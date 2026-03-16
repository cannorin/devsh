# devsh

Opens a shell inside a running VS Code devcontainer, configured the same way VS Code would: correct user, correct working directory.

If run from within the project directory (or a subdirectory of it), devsh automatically selects the matching container and maps the current path into the container. For example, running devsh from `~/Code/myproject/apps/web` will open a shell at `/workspaces/myproject/apps/web` inside the container.

If multiple containers are running and none matches the current directory, an interactive selection menu is shown.

## Why?

I live in a Quake-style terminal and I don't want to use the integrated terminal of VS Code.

## Requirements

- Docker
- The devcontainer must be started by VS Code (it relies on labels VS Code attaches to the container)

## Installation

### Using `go install`

```
go install github.com/cannorin/devsh@latest
```

### Building from source

```
git clone https://github.com/cannorin/devsh
cd devsh
go build -o devsh .
cp devsh ~/.local/bin/
```
