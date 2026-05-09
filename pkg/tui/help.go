package tui

// Help rendering is handled by help.Model embedded in Model (see model.go).
// This file is intentionally minimal — the help.Model in the model struct
// provides both ShortHelp (footer) and FullHelp (overlay) views via KeyMap.