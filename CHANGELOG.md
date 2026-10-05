# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/).

## [0.6.1](https://github.com/rvben/werkt/compare/v0.6.0...v0.6.1) - 2026-10-05

### Added

- **approvals**: add a display-only image field ([eb67b5b](https://github.com/rvben/werkt/commit/eb67b5bc587fe7f08de6012c8a8e6b4343a793bc))

### Fixed

- **approvals**: store an approval that requests no fields ([6c31508](https://github.com/rvben/werkt/commit/6c31508be78b2d51bffeb625dfef3b02a0fbd3d7))

## [0.6.0](https://github.com/rvben/werkt/compare/v0.5.1...v0.6.0) - 2026-10-05

### Added

- **workspace**: dark surface hierarchy and a connected automation flow with measured ports, selection-aware inspectors, and direct access to run evidence.

### Fixed

- **workspace**: keep large source lists compact, wrap long historical identifiers, and adapt flow controls to narrow workspaces.
- **workspace**: clear recorded sources when returning to the definition view and distinguish missing logs and results from recorded evidence.

## [0.5.1](https://github.com/rvben/werkt/compare/v0.5.0...v0.5.1) - 2026-09-28

### Fixed

- **serve**: drain running attempts on shutdown and requeue them ([f454823](https://github.com/rvben/werkt/commit/f454823303d94933fdff336c207e3033c4142415))
- **husker**: retry rate-limited requests and stop leaking VMs ([5278113](https://github.com/rvben/werkt/commit/527811353c239d189f609e476a3e8af697b5be92))

## [0.5.0](https://github.com/rvben/werkt/compare/v0.4.1...v0.5.0) - 2026-09-21

### Added

- **authoring**: add BYOK Jev workflow drafting ([27e1090](https://github.com/rvben/werkt/commit/27e1090348b557b01857e3a79a88a830e3a7736a))
- **investigations**: retain supervised cloud handoffs and issue ownership ([38a9dd4](https://github.com/rvben/werkt/commit/38a9dd417c3a1f847b621b7960c6273659424688))
- **runner**: make automation state capacity configurable ([f5f97b9](https://github.com/rvben/werkt/commit/f5f97b982cb5e40ffc6ca513244c4897f052a926))

### Fixed

- **sdk**: preserve durable work and reject conflicting controls ([00a618c](https://github.com/rvben/werkt/commit/00a618cc2f409519c5340a474e1da2ca3b5ed201))

## [0.4.1](https://github.com/rvben/werkt/compare/v0.4.0...v0.4.1) - 2026-09-16

### Fixed

- **sdk**: say who a connector request is from ([d8bc7d3](https://github.com/rvben/werkt/commit/d8bc7d31dd0193d86380cb219bc6accc6bd9c68b))

## [0.4.0](https://github.com/rvben/werkt/compare/v0.3.2...v0.4.0) - 2026-09-14

### Added

- **workspace**: clarify automation operations UI ([b6d8e63](https://github.com/rvben/werkt/commit/b6d8e634e7ded669ff704b57c96853143c5f002d))
- **notifications**: support automation messages ([05e12d5](https://github.com/rvben/werkt/commit/05e12d5fb9e07c4a2282f0fb13970c729dd25a8e))
- **notifications**: add native Pushover delivery ([2121b50](https://github.com/rvben/werkt/commit/2121b505d092abcd226c2f88ff9de1f18d8037e3))
- **notifications**: add durable provider routing ([3da6b95](https://github.com/rvben/werkt/commit/3da6b95c08556dc1bbf403ce9dff232e41b2242e))
- **openai**: stream long audio transcriptions ([58a7fde](https://github.com/rvben/werkt/commit/58a7fdee1a9d853309d013e1e1db01c4ad4a1a03))
- **openai**: support multi-language transcription hints ([fbc438a](https://github.com/rvben/werkt/commit/fbc438a37008b8b0b7ee728a7ac20ce9ae93fae5))
- **openai**: allow transcription timeout overrides ([1e894ae](https://github.com/rvben/werkt/commit/1e894ae815079e9240267c05e5d490f997b42eb3))
- **runtime**: add verified FFmpeg tool ([98e66e7](https://github.com/rvben/werkt/commit/98e66e721ad57a44f38c8c4c70b97d70158e7a3c))
- **runtime**: resolve tools into immutable images ([6b1ed3d](https://github.com/rvben/werkt/commit/6b1ed3d19e6467d104ccf76b2b4db67dae86c8fe))

### Fixed

- **deployments**: identify a deployment by its package contents ([19d9dd4](https://github.com/rvben/werkt/commit/19d9dd41acbd7259e7a52546a0b96bab1ab343f5))
- **zoom**: authorize recording downloads by header ([02f37ce](https://github.com/rvben/werkt/commit/02f37ceb45e62b5f7398ca74fa199d863bec45f7))
- **webhooks**: accept provider-issued Zoom tokens ([9f2f451](https://github.com/rvben/werkt/commit/9f2f451f98df081bcce5dbf9e8633f439377f49b))
- **provenance**: verify tool environment v2 ([9c7d3b5](https://github.com/rvben/werkt/commit/9c7d3b5ffd357aeb8fa3bbaaa003c263366dc6d6))
- **runtime**: flush images before committing ([8bde302](https://github.com/rvben/werkt/commit/8bde3029cd659c5515291b495a5b07e6da0edf2d))
- **runtime**: persist published tool wrappers ([6c3737a](https://github.com/rvben/werkt/commit/6c3737a27ff582e675bb14985027ddc470448690))
- **runtime**: bind executables to catalog paths ([d5939ca](https://github.com/rvben/werkt/commit/d5939cad7275e6c41b5cb9c999406533ddfb6177))
- **runtime**: generate mise shims explicitly ([3e893c1](https://github.com/rvben/werkt/commit/3e893c106afd92c5000fce6b00845e010e11eb91))
- **runtime**: use portable static FFmpeg ([3be3df1](https://github.com/rvben/werkt/commit/3be3df10e2654186e5cb4edbf76e21d9bbdeb8d1))
- **runtime**: accept Husker stop response ([3717d03](https://github.com/rvben/werkt/commit/3717d03653377848ab77d4a478bde95b635024bd))
- **runtime**: allow Sigstore trust verification ([8a3c74e](https://github.com/rvben/werkt/commit/8a3c74ef3dbd24d653be863db642b22a9c429740))
- **runtime**: allow Python attestation verification ([07ec863](https://github.com/rvben/werkt/commit/07ec86306e78fe96ca586658530b7a0153cae2ea))
- **runtime**: avoid exhausting Husker upload limits ([0e1306d](https://github.com/rvben/werkt/commit/0e1306dbd281f0e8011b608843547ac9c2c048cc))

## [0.3.2](https://github.com/rvben/werkt/compare/v0.3.1...v0.3.2) - 2026-09-14

### Added

- **workspace**: add code-first automation flow ([c2f84ce](https://github.com/rvben/werkt/commit/c2f84ce6d23f0f5a07ee2bea82b63957e5dd74d0))

### Fixed

- **database**: record storage locations relative to the data directory ([6480191](https://github.com/rvben/werkt/commit/6480191a131b9cc2e3c1f3ba2a79acaf013c636b))
