# ccpulse

A native Go TUI dashboard for Claude Code usage. Reads
`~/.claude/projects/*/*.jsonl` transcripts, computes token / cost /
5-hour-window breakdowns, integrates with tmux. Local-only.

## Install

```sh
git clone https://github.com/martinciu/ccpulse ~/code/ccpulse
cd ~/code/ccpulse && mise install && make install
```

The binary lands in `~/.local/bin/ccpulse`.

## License

MIT (see `LICENSE`).
