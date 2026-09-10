#!/usr/bin/env python3
"""Doubao streaming dictation for Omarchy.

A small daemon that records the microphone with ``parecord``, streams raw PCM to
Doubao's streaming ASR websocket API, and types the transcription into the
focused window with ``wtype`` while you speak.

It deliberately bypasses voxtype so it can emit text live; Hyprland bindings call
``doubao-dictate start|stop|toggle|cancel`` which talk to the daemon over a unix
socket.
"""

from __future__ import annotations

import argparse
import asyncio
import io
import json
import os
import signal
import struct
import subprocess
import sys
import time
from dataclasses import dataclass
from pathlib import Path

import aiohttp
from loguru import logger

import protocol

APP = "doubao-dictate"
RUNTIME_DIR = Path(os.environ.get("XDG_RUNTIME_DIR", f"/tmp/{APP}"))
SOCK_PATH = RUNTIME_DIR / f"{APP}.sock"
STATE_PATH = RUNTIME_DIR / f"{APP}.state"
STATE_DIR = Path(
    os.environ.get("XDG_STATE_HOME", Path.home() / ".local/state")
) / APP
# Config lives in a .env next to this script (real env vars override it).
ENV_PATH = Path(
    os.environ.get("DOUBAO_DICTATE_ENV") or Path(__file__).resolve().parent / ".env"
)

MAX_BACKSPACES = 500


# --------------------------------------------------------------------------- #
# Config
# --------------------------------------------------------------------------- #
@dataclass
class Config:
    url: str = "wss://openspeech.bytedance.com/api/v3/sauc/bigmodel"
    api_key: str = ""
    resource_id: str = "volc.seedasr.sauc.duration"
    device: str = "default"
    sample_rate: int = 16000
    segment_ms: int = 200
    max_duration_secs: int = 120
    enable_nonstream: bool = False
    recognize: str = "batch"  # "batch" (record fully, then send once) | "stream"
    batch_url: str = "wss://openspeech.bytedance.com/api/v3/sauc/bigmodel_nostream"
    mode: str = "type"  # "type" | "clipboard"
    stream_typing: bool = True
    flush_delay_ms: int = 350
    notify: bool = True
    sound: bool = False
    save_audio: bool = False

    @property
    def segment_bytes(self) -> int:
        channels = 2  # s16le
        return channels * self.sample_rate * self.segment_ms // 1000


def read_env_file(path: Path) -> dict[str, str]:
    """Parse a KEY=VALUE .env file. Ignores blanks and # comments."""
    values: dict[str, str] = {}
    try:
        lines = path.read_text().splitlines()
    except OSError:
        return values
    for line in lines:
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[len("export "):]
        key, sep, value = line.partition("=")
        if not sep:
            continue
        key = key.strip()
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in ("'", '"'):
            value = value[1:-1]
        values[key] = value
    return values


def load_config() -> Config:
    cfg = Config()
    file_env = read_env_file(ENV_PATH)
    if ENV_PATH.is_file():
        logger.info(f"loaded config from {ENV_PATH}")
    else:
        logger.warning(f"no config file at {ENV_PATH}; relying on environment variables")

    def raw(key: str) -> str | None:
        # Real environment variables win over the .env file.
        value = os.environ.get(key)
        return value if value is not None else file_env.get(key)

    def get(key: str, default: str) -> str:
        value = raw(key)
        return value if value not in (None, "") else default

    def get_int(key: str, default: int) -> int:
        value = raw(key)
        try:
            return int(value) if value not in (None, "") else default
        except ValueError:
            logger.warning(f"invalid integer for {key}={value!r}; using {default}")
            return default

    def get_bool(key: str, default: bool) -> bool:
        value = raw(key)
        if value in (None, ""):
            return default
        return value.strip().lower() in ("1", "true", "yes", "on")

    cfg.api_key = get("DOUBAO_API_KEY", cfg.api_key)
    cfg.url = get("DOUBAO_URL", cfg.url)
    cfg.batch_url = get("DOUBAO_BATCH_URL", cfg.batch_url)
    cfg.resource_id = get("DOUBAO_RESOURCE_ID", cfg.resource_id)
    cfg.device = get("DOUBAO_DEVICE", cfg.device)
    cfg.sample_rate = get_int("DOUBAO_SAMPLE_RATE", cfg.sample_rate)
    cfg.segment_ms = get_int("DOUBAO_SEGMENT_MS", cfg.segment_ms)
    cfg.max_duration_secs = get_int("DOUBAO_MAX_DURATION_SECS", cfg.max_duration_secs)
    cfg.recognize = get("DOUBAO_RECOGNIZE", cfg.recognize)
    cfg.enable_nonstream = get_bool("DOUBAO_ENABLE_NONSTREAM", cfg.enable_nonstream)
    cfg.mode = get("DOUBAO_MODE", cfg.mode)
    cfg.stream_typing = get_bool("DOUBAO_STREAM_TYPING", cfg.stream_typing)
    cfg.flush_delay_ms = get_int("DOUBAO_FLUSH_DELAY_MS", cfg.flush_delay_ms)
    cfg.notify = get_bool("DOUBAO_NOTIFY", cfg.notify)
    cfg.sound = get_bool("DOUBAO_SOUND", cfg.sound)
    cfg.save_audio = get_bool("DOUBAO_SAVE_AUDIO", cfg.save_audio)
    return cfg


# --------------------------------------------------------------------------- #
# Small helpers
# --------------------------------------------------------------------------- #
def common_prefix_len(a: str, b: str) -> int:
    n = min(len(a), len(b))
    i = 0
    while i < n and a[i] == b[i]:
        i += 1
    return i


def write_state(state: str) -> None:
    try:
        RUNTIME_DIR.mkdir(parents=True, exist_ok=True)
        STATE_PATH.write_text(state)
    except OSError as e:
        logger.debug(f"could not write state file: {e}")


def notify(title: str, body: str = "", urgency: str = "normal") -> None:
    try:
        args = ["notify-send", "-u", urgency, title]
        if body:
            args.append(body)
        subprocess.Popen(
            args,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True,
        )
    except Exception as e:  # pragma: no cover
        logger.debug(f"notify failed: {e}")


async def exec_cmd(args: list[str], data: bytes | None = None) -> int:
    proc = await asyncio.create_subprocess_exec(
        *args,
        stdin=asyncio.subprocess.PIPE if data is not None else asyncio.subprocess.DEVNULL,
        stdout=asyncio.subprocess.DEVNULL,
        stderr=asyncio.subprocess.DEVNULL,
    )
    if data is not None and proc.stdin is not None:
        proc.stdin.write(data)
        await proc.stdin.drain()
        proc.stdin.close()
    return await proc.wait()


def _wav_header(sample_rate: int, channels: int, bits: int, data_len: int) -> bytes:
    byte_rate = sample_rate * channels * bits // 8
    block_align = channels * bits // 8
    return (
        b"RIFF"
        + struct.pack("<I", 36 + data_len)
        + b"WAVEfmt "
        + struct.pack("<IHHIIHH", 16, 1, channels, sample_rate, byte_rate, block_align, bits)
        + b"data"
        + struct.pack("<I", data_len)
    )


class WavWriter:
    """Streams PCM to a WAV file, patching the header on close."""

    def __init__(self, path: Path, sample_rate: int, channels: int = 1, bits: int = 16):
        self.path = path
        self.sample_rate = sample_rate
        self.channels = channels
        self.bits = bits
        self.total = 0
        path.parent.mkdir(parents=True, exist_ok=True)
        self._f = open(path, "wb")
        self._f.write(b"\x00" * 44)

    def write(self, data: bytes) -> None:
        self._f.write(data)
        self.total += len(data)

    def close(self) -> None:
        if self._f is None:
            return
        try:
            self._f.seek(0)
            self._f.write(_wav_header(self.sample_rate, self.channels, self.bits, self.total))
        finally:
            self._f.close()
            self._f = None


def prune_sessions(keep: int = 20) -> None:
    sessions = STATE_DIR / "sessions"
    if not sessions.is_dir():
        return
    files = sorted(sessions.glob("*.wav"), key=lambda p: p.stat().st_mtime, reverse=True)
    for old in files[keep:]:
        try:
            old.unlink()
        except OSError:
            pass


# --------------------------------------------------------------------------- #
# Typing with reconciliation
# --------------------------------------------------------------------------- #
class Typist:
    """Types text into the focused window, reconciling revisions via backspace."""

    def __init__(self, cfg: Config):
        self.cfg = cfg
        self.applied = ""
        self.target = ""
        self.live = cfg.stream_typing and cfg.mode == "type"
        self._wake = asyncio.Event()
        self._task: asyncio.Task | None = None
        self._warned = False

    async def start(self) -> None:
        if self.live:
            self._task = asyncio.create_task(self._loop())

    def set_target(self, text: str) -> None:
        self.target = text
        if self.live:
            self._wake.set()

    async def _loop(self) -> None:
        try:
            while True:
                await self._wake.wait()
                self._wake.clear()
                await asyncio.sleep(self.cfg.flush_delay_ms / 1000)
                await self._apply(self.target)
        except asyncio.CancelledError:
            pass

    async def flush(self) -> None:
        if self._task:
            self._task.cancel()
            try:
                await self._task
            except asyncio.CancelledError:
                pass
            self._task = None
        await self._apply(self.target, force=True)

    async def _apply(self, target: str, force: bool = False) -> None:
        if target == self.applied:
            return
        if self.cfg.mode == "none":
            self.applied = target
            return
        if self.cfg.mode == "clipboard":
            if target:
                await exec_cmd(["wl-copy"], target.encode())
            self.applied = target
            return

        common = common_prefix_len(self.applied, target)
        back = len(self.applied) - common
        if back > 0:
            back = min(back, MAX_BACKSPACES)
            args = ["wtype"] + ["-k", "BackSpace"] * back
            await exec_cmd(args)
            if back == MAX_BACKSPACES:
                logger.warning("hit backspace cap; output may be out of sync")
        add = target[common:]
        if add:
            rc = await exec_cmd(["wtype", "-"], add.encode())
            if rc != 0 and not self._warned:
                self._warned = True
                logger.warning(
                    "wtype failed (rc=%s); is WAYLAND_DISPLAY set for the daemon?", rc
                )
        self.applied = target


# --------------------------------------------------------------------------- #
# Streaming session
# --------------------------------------------------------------------------- #
class Session:
    def __init__(self, cfg: Config):
        self.cfg = cfg
        self.typist = Typist(cfg)
        self.recorder: asyncio.subprocess.Process | None = None
        self._aborted = False
        self._final_text = ""
        self._stop_event: asyncio.Event | None = None
        self._max_duration: float | None = None
        self._wav: WavWriter | None = None
        self._responses: list[str] = []

    # -- lifecycle helpers -------------------------------------------------- #
    def _audio_payload(self, enable_nonstream: bool) -> dict:
        return {
            "user": {"uid": "doubao-dictate"},
            "audio": {
                "format": "pcm",
                "codec": "raw",
                "rate": self.cfg.sample_rate,
                "bits": 16,
                "channel": 1,
            },
            "request": {
                "model_name": "bigmodel",
                "enable_itn": True,
                "enable_punc": True,
                "enable_ddc": True,
                "show_utterances": True,
                "enable_nonstream": enable_nonstream,
            },
        }

    async def _terminate_recorder(self) -> None:
        if self.recorder and self.recorder.returncode is None:
            try:
                self.recorder.terminate()
            except ProcessLookupError:
                pass
            try:
                await asyncio.wait_for(self.recorder.wait(), timeout=2)
            except asyncio.TimeoutError:
                self.recorder.kill()

    def abort(self) -> None:
        """Cancel the current recording and discard its text."""
        self._aborted = True
        if self._stop_event:
            self._stop_event.set()

    # -- main flow ---------------------------------------------------------- #
    async def run(self, source, stop_event: asyncio.Event, realtime: bool = True,
                  from_file: bool = False) -> str:
        self._stop_event = stop_event
        if not self.cfg.api_key:
            raise RuntimeError("no API key configured (set DOUBAO_API_KEY, or put it in .env)")

        batch = self.cfg.recognize == "batch" and not from_file
        self._max_duration = None if from_file else float(self.cfg.max_duration_secs)

        await self.typist.start()
        if batch:
            # Batch mode: record the whole utterance, then one final result.
            self.typist.live = False
        if self.cfg.save_audio and not from_file:
            stamp = time.strftime("%Y%m%d-%H%M%S")
            self._wav = WavWriter(
                STATE_DIR / "sessions" / f"{stamp}.wav", self.cfg.sample_rate
            )
            prune_sessions()

        try:
            if batch:
                pcm = await self._record(source, stop_event)
                seconds = len(pcm) / 2 / self.cfg.sample_rate
                logger.info(f"recorded {seconds:.1f}s of audio; recognizing in one shot")
                if self._aborted:
                    return ""
                self._max_duration = None
                await self._run_ws(
                    io.BytesIO(pcm),
                    asyncio.Event(),
                    realtime=False,
                    from_file=True,
                    watch=False,
                    url=self._batch_url(),
                    enable_nonstream=False,
                )
            else:
                await self._run_ws(
                    source,
                    stop_event,
                    realtime=realtime,
                    from_file=from_file,
                    watch=True,
                    url=self.cfg.url,
                    enable_nonstream=self.cfg.enable_nonstream,
                )

            if not self._aborted:
                await self.typist.flush()
            return self._final_text
        finally:
            await self._terminate_recorder()
            if self._wav is not None:
                self._wav.close()
                logger.info(f"saved session audio: {self._wav.path} ({self._wav.total} bytes)")
            if self._responses:
                logger.debug("response timeline: " + " | ".join(self._responses))

    def _batch_url(self) -> str:
        if self.cfg.batch_url:
            return self.cfg.batch_url
        return self.cfg.url.replace("/bigmodel", "/bigmodel_nostream")

    async def _record(self, source, stop_event: asyncio.Event) -> bytes:
        """Buffer microphone PCM until stop, max duration, or EOF."""
        buf = bytearray()
        seg = self.cfg.segment_bytes
        watch = asyncio.create_task(self._watch_stop(stop_event))
        try:
            while True:
                try:
                    chunk = await source.readexactly(seg)
                except asyncio.IncompleteReadError as e:
                    if e.partial:
                        buf += e.partial
                        if self._wav is not None:
                            self._wav.write(e.partial)
                    break
                buf += chunk
                if self._wav is not None:
                    self._wav.write(chunk)
        finally:
            watch.cancel()
            await asyncio.gather(watch, return_exceptions=True)
            await self._terminate_recorder()
        return bytes(buf)

    async def _run_ws(self, source, stop_event: asyncio.Event, *, realtime: bool,
                      from_file: bool, watch: bool, url: str, enable_nonstream: bool) -> None:
        session = aiohttp.ClientSession(
            timeout=aiohttp.ClientTimeout(total=None, connect=15, sock_connect=15)
        )
        conn = None
        try:
            conn = await session.ws_connect(
                url,
                headers=protocol.RequestBuilder.new_auth_headers(
                    protocol.Config(api_key=self.cfg.api_key, resource_id=self.cfg.resource_id)
                ),
                heartbeat=None,
            )
            logger.info(f"connected to {url}")
            await conn.send_bytes(
                protocol.RequestBuilder.new_full_client_request(
                    1, self._audio_payload(enable_nonstream)
                )
            )
            ack = await asyncio.wait_for(conn.receive(), timeout=10)
            if ack.type == aiohttp.WSMsgType.BINARY:
                parsed = protocol.ResponseParser.parse_response(ack.data)
                logger.debug(f"session ack: {parsed.to_dict()}")

            send_task = asyncio.create_task(self._sender(conn, source, stop_event, realtime, from_file))
            tasks = [send_task]
            if watch:
                tasks.append(asyncio.create_task(self._watch_stop(stop_event)))
            recv_task = asyncio.create_task(self._receiver(conn))
            tasks.append(recv_task)
            try:
                await asyncio.wait({recv_task, send_task}, return_when=asyncio.FIRST_COMPLETED)

                if recv_task.done() and recv_task.exception():
                    raise recv_task.exception()
                if send_task.done() and send_task.exception():
                    raise send_task.exception()

                if not recv_task.done():
                    # Sender flushed its final packet; wait for the server's last response.
                    try:
                        await asyncio.wait_for(asyncio.shield(recv_task), timeout=15)
                    except asyncio.TimeoutError:
                        logger.error("no final response from server")
            finally:
                for task in tasks:
                    task.cancel()
                await asyncio.gather(*tasks, return_exceptions=True)
        finally:
            if conn is not None:
                await conn.close()
            await session.close()

    async def _watch_stop(self, stop_event: asyncio.Event) -> None:
        if self._max_duration:
            try:
                await asyncio.wait_for(stop_event.wait(), timeout=self._max_duration)
            except asyncio.TimeoutError:
                logger.warning(
                    f"max recording duration ({self._max_duration}s) reached; auto-stopping"
                )
                if self.cfg.notify:
                    notify(
                        "Recording limit reached",
                        f"Auto-stopped at {self._max_duration:.0f}s",
                    )
        else:
            await stop_event.wait()
        await self._terminate_recorder()

    async def _sender(self, conn, source, stop_event, realtime, from_file) -> None:
        seg = self.cfg.segment_bytes
        seq = 2
        pending: bytes | None = None

        async def send(chunk: bytes, is_last: bool) -> None:
            await conn.send_bytes(
                protocol.RequestBuilder.new_audio_only_request(seq, chunk, is_last=is_last)
            )

        while not stop_event.is_set():
            try:
                if from_file:
                    chunk = source.read(seg)
                    eof = len(chunk) < seg
                else:
                    chunk = await source.readexactly(seg)
                    eof = False
            except asyncio.IncompleteReadError as e:
                chunk, eof = e.partial, True

            if chunk:
                if self._wav is not None and not from_file:
                    self._wav.write(chunk)
                if pending is not None:
                    await send(pending, is_last=False)
                    seq += 1
                pending = chunk
            if eof:
                break
            if from_file and realtime:
                await asyncio.sleep(self.cfg.segment_ms / 1000)

        if pending is not None:
            await send(pending, is_last=True)
        else:
            await send(b"", is_last=True)
        logger.debug("sent final audio packet")

    async def _receiver(self, conn) -> None:
        while True:
            try:
                msg = await asyncio.wait_for(conn.receive(), timeout=30)
            except asyncio.TimeoutError:
                logger.error("timed out waiting for ASR response")
                break

            if msg.type != aiohttp.WSMsgType.BINARY:
                if msg.type in (aiohttp.WSMsgType.CLOSED, aiohttp.WSMsgType.CLOSING):
                    logger.info("websocket closed by server")
                    break
                if msg.type == aiohttp.WSMsgType.ERROR:
                    logger.error(f"websocket error: {msg.data}")
                    break
                continue

            resp = protocol.ResponseParser.parse_response(msg.data)
            if resp.code != 0:
                detail = resp.payload_msg or resp.to_dict()
                logger.error(f"asr error code={resp.code}: {detail}")
                raise RuntimeError(f"ASR error {resp.code}")

            result = (resp.payload_msg or {}).get("result", {})
            text = result.get("text") or ""
            if text:
                if text != self._final_text:
                    self._responses.append(text)
                    logger.debug(f"partial: {text!r}")
                self._final_text = text
                self.typist.set_target(text)

            if resp.is_last_package:
                logger.info(f"final transcript: {text!r}")
                break


# --------------------------------------------------------------------------- #
# Daemon
# --------------------------------------------------------------------------- #
class Daemon:
    def __init__(self, cfg: Config):
        self.cfg = cfg
        self.state = "idle"
        self.session: Session | None = None
        self.task: asyncio.Task | None = None
        self.stop_event: asyncio.Event | None = None

    def _set_state(self, state: str) -> None:
        self.state = state
        write_state(state)

    async def serve(self) -> None:
        RUNTIME_DIR.mkdir(parents=True, exist_ok=True)
        if SOCK_PATH.exists():
            SOCK_PATH.unlink()
        server = await asyncio.start_unix_server(self._handle, path=str(SOCK_PATH))
        self._set_state("idle")
        logger.info(f"daemon listening on {SOCK_PATH}")
        if self.cfg.notify:
            notify("Doubao dictation ready", "Press F9 to dictate")
        async with server:
            await server.serve_forever()

    async def _handle(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        try:
            line = await asyncio.wait_for(reader.readline(), timeout=5)
            cmd = line.decode().strip()
            logger.info(f"command received: {cmd!r}")
            result = await self.command(cmd)
        except Exception as e:  # pragma: no cover
            result = {"ok": False, "error": str(e)}
        try:
            writer.write((json.dumps(result) + "\n").encode())
            await writer.drain()
        finally:
            writer.close()

    async def command(self, cmd: str) -> dict:
        if cmd == "ping":
            return {"ok": True, "state": self.state}
        if cmd == "status":
            return {"ok": True, "state": self.state}
        if cmd == "start":
            return await self._start()
        if cmd == "stop":
            return await self._stop()
        if cmd == "toggle":
            return await self._stop() if self.state != "idle" else await self._start()
        if cmd == "cancel":
            return await self._cancel()
        return {"ok": False, "error": f"unknown command {cmd!r}"}

    async def _start(self) -> dict:
        if self.state != "idle":
            return {"ok": False, "state": self.state, "error": "already active"}
        self.stop_event = asyncio.Event()
        self.session = Session(self.cfg)
        self.task = asyncio.create_task(self._run_session())
        self._set_state("recording")
        if self.cfg.notify:
            notify("🎤 Recording", "Press F9 again (or Super+Ctrl+X) to stop")
        return {"ok": True, "state": self.state}

    async def _stop(self) -> dict:
        if self.state == "idle" or not self.stop_event:
            return {"ok": False, "state": self.state, "error": "not recording"}
        self.stop_event.set()
        if self.cfg.notify:
            notify("Transcribing…")
        return {"ok": True, "state": self.state}

    async def _cancel(self) -> dict:
        if self.state == "idle" or not self.session:
            return {"ok": False, "state": self.state, "error": "not recording"}
        self.session.abort()
        if self.task:
            self.task.cancel()
        self._set_state("idle")
        return {"ok": True, "state": self.state}

    async def _run_session(self) -> None:
        assert self.session and self.stop_event
        try:
            recorder = await asyncio.create_subprocess_exec(
                *self._recorder_cmd(),
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.DEVNULL,
            )
            self.session.recorder = recorder
            text = await self.session.run(recorder.stdout, self.stop_event)
            if recorder.returncode not in (0, None) and not text:
                logger.error(f"parecord exited with {recorder.returncode}")
                if self.cfg.notify:
                    notify("No microphone input", "Check your default audio source", urgency="critical")
            elif not text and self.cfg.notify:
                notify("No speech detected", urgency="low")
        except asyncio.CancelledError:
            logger.info("session cancelled")
        except Exception as e:
            logger.exception("dictation failed")
            if self.cfg.notify:
                notify("Dictation failed", str(e), urgency="critical")
        finally:
            self._set_state("idle")
            self.session = None
            self.task = None
            self.stop_event = None

    def _recorder_cmd(self) -> list[str]:
        cmd = [
            "parecord",
            "--raw",
            "--format=s16le",
            f"--rate={self.cfg.sample_rate}",
            "--channels=1",
            "--latency-msec=100",
        ]
        if self.cfg.device and self.cfg.device != "default":
            cmd += ["-d", self.cfg.device]
        return cmd

    async def shutdown(self) -> None:
        if self.session:
            self.session.abort()
        if self.task:
            self.task.cancel()
            try:
                await self.task
            except (asyncio.CancelledError, Exception):
                pass
        if self.session:
            await self.session._terminate_recorder()


# --------------------------------------------------------------------------- #
# Client CLI
# --------------------------------------------------------------------------- #
def send_command(cmd: str) -> dict:
    import socket as _socket

    if not SOCK_PATH.exists():
        return {"ok": False, "error": "daemon not running"}
    try:
        with _socket.socket(_socket.AF_UNIX, _socket.SOCK_STREAM) as s:
            s.settimeout(5)
            s.connect(str(SOCK_PATH))
            s.sendall((cmd + "\n").encode())
            data = b""
            while not data.endswith(b"\n"):
                chunk = s.recv(4096)
                if not chunk:
                    break
                data += chunk
        return json.loads(data.decode() or "{}")
    except Exception as e:
        return {"ok": False, "error": str(e)}


def pcm_from_file(path: str, sample_rate: int) -> bytes:
    cmd = [
        "ffmpeg", "-nostdin", "-v", "quiet", "-y", "-i", path,
        "-acodec", "pcm_s16le", "-ac", "1", "-ar", str(sample_rate),
        "-f", "s16le", "-",
    ]
    return subprocess.run(
        cmd, check=True, capture_output=True, stdin=subprocess.DEVNULL
    ).stdout


async def run_transcribe(args) -> int:
    cfg = load_config()
    if args.device:
        cfg.device = args.device
    if args.mode:
        cfg.mode = args.mode
    if args.nonstream:
        cfg.enable_nonstream = True
    if not args.type:
        cfg.mode = "none"  # printing only, do not touch the focused window

    pcm = pcm_from_file(args.file, cfg.sample_rate)
    source = io.BytesIO(pcm)
    stop_event = asyncio.Event()
    session = Session(cfg)
    t0 = time.time()
    text = await session.run(
        source, stop_event, realtime=not args.fast, from_file=True
    )
    if args.print or not args.type:
        print(f"=== transcript ({time.time() - t0:.1f}s) ===\n{text}")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(prog=APP, description="Doubao streaming dictation")
    sub = parser.add_subparsers(dest="cmd")

    sub.add_parser("daemon", help="run the dictation daemon")
    sub.add_parser("start", help="start recording")
    sub.add_parser("stop", help="stop recording and finalize")
    sub.add_parser("toggle", help="toggle recording")
    sub.add_parser("cancel", help="cancel recording, discard text")
    sub.add_parser("status", help="show daemon state")

    t = sub.add_parser("transcribe", help="transcribe an audio file (for testing)")
    t.add_argument("file")
    t.add_argument("--print", action="store_true", help="print the transcript")
    t.add_argument("--type", action="store_true", help="type the transcript instead of printing")
    t.add_argument("--fast", action="store_true", help="do not pace audio in realtime")
    t.add_argument("--nonstream", action="store_true", help="enable the non-stream second pass")
    t.add_argument("--device")
    t.add_argument("--mode")

    args = parser.parse_args()

    STATE_DIR.mkdir(parents=True, exist_ok=True)
    logger.remove()
    logger.add(sys.stderr, level="INFO")
    logger.add(STATE_DIR / "daemon.log", level="DEBUG", rotation="5 MB", retention=3)

    if args.cmd in (None, "daemon"):
        cfg = load_config()
        daemon = Daemon(cfg)

        loop = asyncio.new_event_loop()
        asyncio.set_event_loop(loop)
        stop = asyncio.Event()
        for sig in (signal.SIGINT, signal.SIGTERM):
            loop.add_signal_handler(sig, stop.set)

        async def _runner() -> None:
            server_task = asyncio.create_task(daemon.serve())
            stop_task = asyncio.create_task(stop.wait())
            done, pending = await asyncio.wait(
                {server_task, stop_task}, return_when=asyncio.FIRST_COMPLETED
            )
            if server_task in done and server_task.exception():
                for task in pending:
                    task.cancel()
                await asyncio.gather(*pending, return_exceptions=True)
                raise server_task.exception()
            for task in pending:
                task.cancel()
            await asyncio.gather(server_task, stop_task, return_exceptions=True)

        try:
            loop.run_until_complete(_runner())
        except KeyboardInterrupt:
            pass
        finally:
            try:
                loop.run_until_complete(daemon.shutdown())
            except Exception as e:  # pragma: no cover
                logger.warning(f"shutdown cleanup failed: {e!r}")
            write_state("idle")
            try:
                SOCK_PATH.unlink()
            except OSError:
                pass
            loop.close()
        return 0

    if args.cmd == "transcribe":
        return asyncio.run(run_transcribe(args))

    result = send_command(args.cmd)
    print(json.dumps(result, ensure_ascii=False))
    return 0 if result.get("ok") else 1


if __name__ == "__main__":
    sys.exit(main())
