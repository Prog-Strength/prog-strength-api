# [0.22.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.21.1...v0.22.0) (2026-05-19)


### Features

* **telemetry:** daily TTL purge of aged message and tool-call content ([ac657f7](https://github.com/Prog-Strength/prog-strength-api/commit/ac657f727d908eeea42bb87fc7ca8d10f0b3d827))

## [0.21.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.21.0...v0.21.1) (2026-05-18)


### Bug Fixes

* **server:** register /metrics after all middleware so prod doesn't panic ([2da79e8](https://github.com/Prog-Strength/prog-strength-api/commit/2da79e86ce5aa40b946ec31fdf34771731898eec))

# [0.21.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.20.0...v0.21.0) (2026-05-18)


### Features

* **telemetry:** foundation for agent observability ([42ed81c](https://github.com/Prog-Strength/prog-strength-api/commit/42ed81cbd330ef396c933dca108470f23b1ba5eb))

# [0.20.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.19.1...v0.20.0) (2026-05-18)


### Features

* **workouts:** paginated GET /workouts with timeframe filters ([c5dd12e](https://github.com/Prog-Strength/prog-strength-api/commit/c5dd12e8fd48311bbbd469041179144cd929b93e))

## [0.19.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.19.0...v0.19.1) (2026-05-18)


### Bug Fixes

* **workouts:** skip bodyweight sets when recomputing personal records ([45b5df0](https://github.com/Prog-Strength/prog-strength-api/commit/45b5df0c2ee6bdb8ffb9ec81e3f869d796d3426a))

# [0.19.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.18.0...v0.19.0) (2026-05-18)


### Features

* **workouts:** personal records + PR break event log ([d6002cf](https://github.com/Prog-Strength/prog-strength-api/commit/d6002cfb3c892c7e1f74eea4be4af778f5a1c8cf))

# [0.18.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.17.0...v0.18.0) (2026-05-18)


### Features

* **exercises:** add Dumbbell Reverse Lunge, Leg Press, Calf Press ([39bfb5a](https://github.com/Prog-Strength/prog-strength-api/commit/39bfb5a0ad5e08f77ce0decf18d9fc725aa697cd))

# [0.17.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.16.0...v0.17.0) (2026-05-18)


### Features

* **exercises:** add Standing Cable Fly to catalog ([6561eac](https://github.com/Prog-Strength/prog-strength-api/commit/6561eac3f6dc7464bae3e8cc11e8a25622caca98))

# [0.16.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.15.0...v0.16.0) (2026-05-18)


### Features

* **exercises:** add Neutral Grip Dumbbell Incline Row to catalog ([2b0bd8d](https://github.com/Prog-Strength/prog-strength-api/commit/2b0bd8d0801b064502bcb08efd995d6ba3f4faec))

# [0.15.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.14.0...v0.15.0) (2026-05-18)


### Features

* **progress:** base 1RM baseline on per-workout max, not avg ([8e62d41](https://github.com/Prog-Strength/prog-strength-api/commit/8e62d413c1eb3d4ed9c393afdb923f3c3f4524d1))

# [0.14.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.13.0...v0.14.0) (2026-05-18)


### Features

* **workouts:** muscle-group progression at /workouts/progression ([490038c](https://github.com/Prog-Strength/prog-strength-api/commit/490038c929f250b01899c2c1debac8108ada3d8a))

# [0.13.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.12.0...v0.13.0) (2026-05-18)


### Features

* **workouts:** persist estimated 1RM history per workout exercise ([40b3218](https://github.com/Prog-Strength/prog-strength-api/commit/40b3218a470c2c0a2b212152d2592d933133ee1d))

# [0.12.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.11.0...v0.12.0) (2026-05-17)


### Features

* **exercises:** Add additional exercises to the catalog ([a13fdf2](https://github.com/Prog-Strength/prog-strength-api/commit/a13fdf272ba5b08b718624caf1bcc7d57f08d13f))

# [0.11.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.10.0...v0.11.0) (2026-05-17)


### Features

* **workouts:** GET /workouts/progression endpoint ([dd8a481](https://github.com/Prog-Strength/prog-strength-api/commit/dd8a481a28406fc254ed421bb3a1199ce48c821d))

# [0.10.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.9.1...v0.10.0) (2026-05-17)


### Features

* **auth:** add BETA_ALLOWED_EMAILS gate at OAuth callback ([73bff12](https://github.com/Prog-Strength/prog-strength-api/commit/73bff12bbaa7a72bef3cd7ba3c76a0379f3407cb))

## [0.9.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.9.0...v0.9.1) (2026-05-17)


### Bug Fixes

* **api:** pass RETURN_TO_ALLOWED_ORIGINS to api container ([682be35](https://github.com/Prog-Strength/prog-strength-api/commit/682be3549dc609c6d24f7dade3a08f26d57bcc7c))

# [0.9.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.8.2...v0.9.0) (2026-05-17)


### Features

* **auth:** support return_to redirect with hash-fragment token ([f0654fd](https://github.com/Prog-Strength/prog-strength-api/commit/f0654fd58cfc90ee7a49d3a9aa59627f477a2f03))

## [0.8.2](https://github.com/Prog-Strength/prog-strength-api/compare/v0.8.1...v0.8.2) (2026-05-17)


### Bug Fixes

* **caddy:** mount config directory instead of single file ([78f132e](https://github.com/Prog-Strength/prog-strength-api/commit/78f132ebf05f20284269efca3da4e652a0680d24))

## [0.8.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.8.0...v0.8.1) (2026-05-16)


### Bug Fixes

* **cicd:** Update release and deploy workflow to use Litestream env vars ([b594837](https://github.com/Prog-Strength/prog-strength-api/commit/b59483745077ac43739bcfaa33318bebcc774ec8))

# [0.8.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.7.1...v0.8.0) (2026-05-16)


### Features

* **db_backups:** Add Litestream sidecar service to take database snapshots ([3077641](https://github.com/Prog-Strength/prog-strength-api/commit/30776412d9094880b0956f8d163cfb18489e5518))

## [0.7.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.7.0...v0.7.1) (2026-05-16)


### Bug Fixes

* **exercises:** Update descriptions for dumbbell exercieses to record weight per-dumbbell ([9c64a28](https://github.com/Prog-Strength/prog-strength-api/commit/9c64a28f4d969dd5eff751e7f182fac2cf9757b7))

# [0.7.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.6.1...v0.7.0) (2026-05-16)


### Features

* **exercises:** Add additional exercises to the catalog ([486fe3a](https://github.com/Prog-Strength/prog-strength-api/commit/486fe3ad4fbd288a449321d5159fe360f3481984))

## [0.6.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.6.0...v0.6.1) (2026-05-16)


### Bug Fixes

* **exercises:** Sync exercise catalog with database ([73ede39](https://github.com/Prog-Strength/prog-strength-api/commit/73ede39d6585a3841890257846c98e3d0ca9d78f))

# [0.6.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.5.0...v0.6.0) (2026-05-16)


### Features

* **exercises:** Add additional chest and back exercises to catalog ([a2e4397](https://github.com/Prog-Strength/prog-strength-api/commit/a2e4397b90d87ba10ca037eef7c14a57fdc23807))

# [0.5.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.4.2...v0.5.0) (2026-05-15)


### Features

* **cicd:** Add a manual deploy workflow ([f9536d0](https://github.com/Prog-Strength/prog-strength-api/commit/f9536d0e17fc81ab8cca833306b6ce7bbc961f4f))

## [0.4.2](https://github.com/Prog-Strength/prog-strength-api/compare/v0.4.1...v0.4.2) (2026-05-15)


### Bug Fixes

* **cicd:** Update release workflow to abort and fail on the first failing command ([810b7c3](https://github.com/Prog-Strength/prog-strength-api/commit/810b7c3da84bd995c5bab16cb8222d208a550928))

## [0.4.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.4.0...v0.4.1) (2026-05-14)


### Bug Fixes

* **deploy:** Join prog-strength shared docker network ([10392a8](https://github.com/Prog-Strength/prog-strength-api/commit/10392a8b685aa6b97f9535c075a761f67df784ed))

# [0.4.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.3.0...v0.4.0) (2026-05-14)


### Features

* **https:** Migrate Caddyfile to infra repository and update release workflow ([2fd5f8f](https://github.com/Prog-Strength/prog-strength-api/commit/2fd5f8f939dc0efab6f77532df2cc97e127599ae))

# [0.3.0](https://github.com/jwallace145/prog-strength/compare/v0.2.0...v0.3.0) (2026-05-10)


### Features

* **workouts:** Add update, read, and delete workout methods ([2902f4e](https://github.com/jwallace145/prog-strength/commit/2902f4e4bddaaee8e2ddb7f16f0a896416e78f7a))

# [0.2.0](https://github.com/jwallace145/prog-strength/compare/v0.1.0...v0.2.0) (2026-05-09)


### Features

* **cicd:** Add automatic versioning to new releases ([bf0b818](https://github.com/jwallace145/prog-strength/commit/bf0b8188bd636138846a45dbf9934c7b07c5807e))
