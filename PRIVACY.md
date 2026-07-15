# TwinTidy privacy notice

TwinTidy is a local desktop utility. It does not require an account and does not include telemetry, analytics, advertising, or automatic cloud upload.

## Data processed locally

To find and review exact duplicates, TwinTidy reads filesystem metadata and selected file bytes, computes hashes, and may ask installed Windows preview providers to render local thumbnails. The current pre-release build does not modify or recycle files; cleanup planning remains local and disabled at the native boundary.

This processing stays on the computer. TwinTidy does not operate a server that receives filenames, paths, hashes, previews, file contents, or cleanup history.

## Diagnostics

Session logs and crash reports are written under:

```text
%LOCALAPPDATA%\TwinTidy\logs
```

Diagnostics are not transmitted automatically. They can contain application/runtime details and local filesystem context useful for troubleshooting. Review and redact them before sharing. Delete the log directory at any time to remove retained diagnostics; TwinTidy recreates it when next started.

## Preferences

Interface preferences — the last main-window position and the most recently selected scan folder path — are stored locally in `%LOCALAPPDATA%\TwinTidy\settings.json`. They are never transmitted, carry no scan results or file contents, and can be deleted at any time; TwinTidy falls back to defaults when the file is absent or unreadable.

## Windows and third-party handlers

Windows Shell thumbnail providers, document handlers, media components, sync clients, and security software installed on the computer may process files according to their own configuration. TwinTidy does not install or control those providers. Avoid previewing sensitive files or remove the relevant handler if its behavior is unsuitable.

Any future network-connected feature requires explicit opt-in and an updated privacy notice, threat model, and architecture decision record before release.
