# demo/ccpulse.tape — reproducible README demo of the ccpulse TUI.
#
# Regenerate with:  make demo
# (builds a release binary, seeds a synthetic *-dev cache via
# scripts/seedyear, then runs this tape). Records ONLY synthetic, leak-free
# fixtures — never real ~/.claude data and no network calls. See issue #265.
#
# Choreography (issue #403): tour every chart view with a readable pause on
# each, capture the skyline-morph zoom animation, and show a horizontal scroll.
#   u  cycles the chart UNIT:  cost → output → usage
#   z  cycles the ZOOM:        15m → 1h → 24h  (skyline morph in bar views, #393/#394)
#   ← / → scroll the chart horizontally
# See issues #393/#394 (skyline morph) and #399 (last GIF regen).
#
# VHS command reference: https://github.com/charmbracelet/vhs#vhs-command-reference

Output demo/ccpulse.gif

Set Shell bash
Set Theme "rose-pine-moon"
Set FontSize 16
Set Width 1200
Set Height 800
Set Padding 20
Set Framerate 24

# Synthetic, leak-free fixtures. Paths are repo-relative (vhs runs from the
# repo root via `make demo`). `make demo` seeds demo/.cache-dev first.
# CCPULSE_FAKE_QUOTA frames the 5h/7d quota header deterministically.
Env CCPULSE_CACHE_DIR "demo/.cache-dev"
Env CCPULSE_PROJECTS_ROOT "demo/.projects"
Env CCPULSE_FAKE_QUOTA "55,42"

# Launch. Opens on the default COST-bars view at 15m zoom. The opening pause
# frames the bordered header with the side-by-side 5h + 7d quota bars.
Type "./ccpulse"
Enter
Sleep 3s

# Scroll left through the dense 15m cost view — the horizontal pan back in time.
Left@60ms 15
Sleep 1s

# z: zoom 15m → 1h. In a bar view this plays the skyline-morph spring animation
# (#393/#394); the pause lets the morph settle on cost-bars at 1h.
Type "z"
Sleep 2.5s

# u: cycle the unit cost → OUTPUT tokens (zoom stays 1h). Read the token y-axis.
Type "u"
Sleep 3.5s

# z: zoom 1h → 24h, still a bar view → a second skyline morph, now in output-token
# mode (the morph is unit-aware). 24h is dense enough to add in-bar number labels.
Type "z"
Sleep 2.5s

# u: cycle the unit output → USAGE line — the fixed 0–100% quota line chart (24h).
# The longest pause sits on the line view: it is the most visually distinct.
Type "u"
Sleep 5s

# Quit.
Type "q"
Sleep 800ms
