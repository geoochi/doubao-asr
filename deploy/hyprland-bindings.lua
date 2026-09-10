-- Doubao dictation hotkeys (bypasses voxtype).
-- Append this to ~/.config/hypr/bindings.lua, then run: hyprctl reload
-- It first unbinds the keys Omarchy assigns to voxtype, then rebinds them.

hl.unbind("SUPER + CTRL + X")
hl.unbind("F9")

o.bind("SUPER + CTRL + X", "Doubao dictation (toggle)", "doubao-dictate toggle")
o.bind("F9", "Doubao dictation (press to start/stop)", "doubao-dictate toggle")
o.bind("SUPER + CTRL + ESCAPE", "Doubao dictation cancel", "doubao-dictate cancel")
