<div align="center">
    <img alt="JetKVM logo" src="https://jetkvm.com/logo-blue.png" height="28">

### KVM

[Discord](https://jetkvm.com/discord) | [Website](https://jetkvm.com) | [Issues](https://github.com/jetkvm/cloud-api/issues) | [Docs](https://jetkvm.com/docs)

[![Twitter](https://img.shields.io/twitter/url/https/twitter.com/jetkvm.svg?style=social&label=Follow%20%40JetKVM)](https://twitter.com/jetkvm)

[![Go Report Card](https://goreportcard.com/badge/github.com/jetkvm/kvm)](https://goreportcard.com/report/github.com/jetkvm/kvm)

</div>

JetKVM is a high-performance, open-source KVM over IP (Keyboard, Video, Mouse) solution designed for efficient remote management of computers, servers, and workstations. Whether you're dealing with boot failures, installing a new operating system, adjusting BIOS settings, or simply taking control of a machine from afar, JetKVM provides the tools to get it done effectively.

## Features

- **Ultra-low Latency** - 1080p@60FPS video with 30-60ms latency using H.264 encoding. Smooth mouse and keyboard interaction for responsive remote control.
- **Free & Optional Remote Access** - Remote management via JetKVM Cloud using WebRTC.
- **Optional Tailscale Networking** - Built-in Tailscale status and control-server configuration, including custom [Headscale](https://headscale.net/)-compatible endpoints.
- **Open-source software** - Written in Golang on Linux. Easily customizable through SSH access to the JetKVM device.

## Contributing

We welcome contributions from the community! Whether it's improving the firmware, adding new features, or enhancing documentation, your input is valuable. We also have some rules and taboos here, so please read this page and our [Code of Conduct](/CODE_OF_CONDUCT.md) carefully.

## I need help

The best place to search for answers is our [Documentation](https://jetkvm.com/docs). If you can't find the answer there, check our [Discord Server](https://jetkvm.com/discord).

## I want to report an issue

If you've found an issue and want to report it, please check our [Issues](https://github.com/jetkvm/kvm/issues) page. Make sure the description contains information about the firmware version you're using, your platform, and a clear explanation of the steps to reproduce the issue.

# Development

JetKVM is written in Go & TypeScript. with some bits and pieces written in C. An intermediate level of Go & TypeScript knowledge is recommended for comfortable programming.

The project contains two main parts, the backend software that runs on the KVM device and the frontend software that is served by the KVM device, and also the cloud.

For comprehensive development information, including setup, testing, debugging, and contribution guidelines, see **[DEVELOPMENT.md](DEVELOPMENT.md)**.

For quick device development, use the `./dev_deploy.sh` script. It will build the frontend and backend and deploy them to the local KVM device. Run `./dev_deploy.sh --help` for more information.

## Backend

The backend is written in Go and is responsible for the KVM device management, the cloud API and the cloud web.

## Frontend

The frontend is written in React and TypeScript and is served by the KVM device. It has three build targets: `device`, `development` and `production`. Development is used for development of the cloud version on your local machine, device is used for building the frontend for the KVM device and production is used for building the frontend for the cloud.

## AI agent control (MCP)

The [built-in MCP server](mcp/README.md) lets an AI client connect directly to the
device to inspect screenshots, send keyboard/mouse input, mount installation media
and submit scripts on the attached computer. No local companion is needed, and
your browser viewer stays connected. Enable token-based access in Settings →
Access. A Windows installation and setup prompt is included.

With the paired Windows guest companion running, the viewer supports text
copy/paste anywhere on the focused KVM page except local text fields, terminals,
settings dialogs and local controls. Shortcuts activate after the companion
advertises support. Copy sends Ctrl+C and requires a new Windows clipboard
sequence before reading text. Paste transfers text over paired USB and sends
Ctrl+V without Enter. These shortcuts require the remote US key layout. Pasted
line breaks can execute commands in some terminals.

There is no background clipboard polling or clipboard history. A per-viewer
keyed digest tracks successful local delivery and pastes, preserving remote
clipboard formats when local text is unchanged. Reads alone do not change it.
HTTPS browsers can copy in one gesture; HTTP and blocked clipboard permissions
show a **Copy ready text** button. Until delivery, in-page paste preserves the
remote copy. Non-text and empty local clipboards use ordinary guest paste.
Prepared text clears on blur, local editing or device changes. Additional
shortcuts during a transfer show a busy message; they do not paste stale content.
Interactive operations wait at most 200 ms for a busy guest channel.
