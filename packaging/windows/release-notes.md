## Install or upgrade

This release provides Windows x64 packages:

- [Windows x64 installer](@RELEASE_URL@/mcp-language-server_@TAG@_windows_amd64_setup.exe)
- [Portable Windows x64 ZIP](@RELEASE_URL@/mcp-language-server_@TAG@_windows_amd64.zip)
- [SHA256SUMS](@RELEASE_URL@/SHA256SUMS) covers both downloads.

The installer runs for the current user without administrator rights. Use the
absolute executable path shown by Setup as your MCP command. Subsequent
installers reuse the previous installation directory and uninstall entry.
Stop connections using that installation and allow the shared broker to exit
before upgrading; Setup rejects a locked executable without terminating
processes. Restart the MCP connection after installing. There is no self-updater.

For a portable install, extract the ZIP to a permanent directory and configure
your MCP client with the executable's full path. Go is not required to run the
bridge. Install your chosen language server and its SDK/runtime separately.
The installer does not modify MCP configurations or PATH. Both downloads are unsigned.

## Build information

Bridge source: `@COMMIT@`, compiler: @GO_VERSION@, CGO disabled.
The installer wraps the verified ZIP using Inno Setup 6.7.3.
