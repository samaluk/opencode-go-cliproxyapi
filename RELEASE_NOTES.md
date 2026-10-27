## What's Changed

Local contributions retain catalog-file loading, explicit effort metadata, harness environment messages and DeepSeek reasoning across tool turns. Release builds stamp the registered version from the release tag.

### Features

- Support native Gemini models such as Gemini 4 Argon through OpenCode Go's
  Google endpoint. Chat Completions, Messages and Responses clients can use
  text, images, function calls and tool-result continuations, in streaming or
  non-streaming mode. Add `gemini-base-url`, `protocols.gemini`, and `gemini`
  route overrides; retain existing protocol routes and credential scheduling.
- Preserve final usage when Gemini sends a separate usage trailer, and report
  blocked, malformed or truncated streams as errors instead of successful ends.

### Bug Fixes

- Preserve Claude image tool results when routing to Chat Completions: retain images in an associated user message after tool replies, preserve text, tool references and error markers, and include images in session derivation. The selected upstream model must support image input.

Special thanks to @c0mm4nd in #16.

- Fix `tool_result` translation when receiving `tool_reference` content blocks from Claude clients (such as ToolSearch, web search, or web fetch in Claude Code), preventing 500 errors and conversation lockouts on Chat Completions and Responses models like DeepSeek.

Special thanks to @SpiGAndromeda in #14.

## Upgrade Notes

- Replace the old plugin binary with the new release binary.
- Restart CLIProxyAPI after replacing the plugin.

**Full Changelog**: https://github.com/massiveits/opencode-go-cliproxyapi/compare/v0.2.1...v0.2.2
