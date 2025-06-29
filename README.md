# YouTube Data CLI (ytdata)

Export your YouTube data including liked videos, subscriptions, and playlists to JSONL format.

## Quick Start

1. **Build**: `go build -o ytdata`
2. **Init**: `./ytdata init` (alias `setup`, guided setup with OAuth)
3. **Use**: `./ytdata liked`, `./ytdata subscriptions`, or `./ytdata playlists`

## Features

- Export liked videos, subscriptions, and playlists
- JSONL output format for easy processing
- Automatic OAuth2 authentication (no manual code entry)
- Credential persistence and auto-refresh
- Interactive setup with step-by-step guidance

## Commands

Run `ytdata --help` or `ytdata <command> --help` to see available commands and examples.

## Setup Process

Run `ytdata init` (alias: setup) for guided setup:

1. **Google Cloud Project** - Create or use existing project
2. **Enable YouTube Data API v3** - Direct link provided
3. **OAuth2 Credentials** - Create web application credentials:
   - Application type: Web application
   - Name: 'ytdata' (or any name)
   - Add http://localhost:8080/ to 'Authorized redirect URIs'
4. **Download & Place** - Put JSON file in config directory
5. **Authentication Test** - Complete OAuth flow automatically

The tool auto-detects client secrets files (pattern: `client_secret_*.apps.googleusercontent.com.json`) and validates configuration.

## Output Format

All commands export to JSONL format (one JSON object per line):

- **Liked Videos**: Complete video metadata, content details, and statistics (up to 1,000 videos [^1])
- **Subscriptions**: Channel information including subscriber counts and statistics  
- **Playlists**: Your created playlists (not including special playlists like Watch Later, Liked Videos, etc.)

[^1]: The YouTube API seems to have an undocumented limitation that restricts retrieval to approximately 1,000 liked videos, even if you have more on your account.

## Authentication

- **First time**: Browser opens automatically for OAuth flow
- **Subsequent uses**: Automatic authentication with saved credentials
- **Token refresh**: Handles expired tokens automatically

## Troubleshooting

- **Setup issues**: Re-run `ytdata init`
- **Auth failures**: Delete credentials file and re-authenticate
- **API quota**: Wait for daily quota reset
- **Missing data**: Some data is only available via [Google Takeout](https://takeout.google.com)

## Security

- Tool requests read-only YouTube access
- Credentials stored securely in user config directory
