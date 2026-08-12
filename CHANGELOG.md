# [0.116.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.115.0...v0.116.0) (2026-08-12)


### Features

* **weather:** free-text place resolution and per-surface attribution on GET /weather ([#133](https://github.com/Prog-Strength/prog-strength-api/issues/133)) ([4f73fb6](https://github.com/Prog-Strength/prog-strength-api/commit/4f73fb6555db3857a69235f800523d0c7a283543))

# [0.115.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.114.0...v0.115.0) (2026-08-12)


### Features

* **dashboard:** add the resting_hr tile to the catalog and recovery family ([#134](https://github.com/Prog-Strength/prog-strength-api/issues/134)) ([3b94ca6](https://github.com/Prog-Strength/prog-strength-api/commit/3b94ca66786b763faaa2668aae32c1324b402396))

# [0.114.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.113.0...v0.114.0) (2026-08-11)


### Features

* **whoop:** sleep ingestion, sleep tile section, and the read:sleep scope migration ([#132](https://github.com/Prog-Strength/prog-strength-api/issues/132)) ([8771556](https://github.com/Prog-Strength/prog-strength-api/commit/877155608ddcf8b11b5bdd38bce0bdba58a481ca))

# [0.113.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.112.0...v0.113.0) (2026-08-11)


### Features

* **weather:** serve the week and the full hourly strip from the same calls ([#131](https://github.com/Prog-Strength/prog-strength-api/issues/131)) ([9a7ad5b](https://github.com/Prog-Strength/prog-strength-api/commit/9a7ad5b128d663001784d32040645e79902dce2d))

# [0.112.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.111.0...v0.112.0) (2026-08-11)


### Features

* **dashboard:** retire the recovery_trend tile into hrv_balance ([#130](https://github.com/Prog-Strength/prog-strength-api/issues/130)) ([d67141f](https://github.com/Prog-Strength/prog-strength-api/commit/d67141f7da3e94dbf6a82fc59c6c76ea2b897efb))

# [0.111.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.110.0...v0.111.0) (2026-08-10)


### Features

* **dashboard:** per-day recovery bands and baseline drift ([#129](https://github.com/Prog-Strength/prog-strength-api/issues/129)) ([f55e627](https://github.com/Prog-Strength/prog-strength-api/commit/f55e627d633c8d95469111c8d1704415830e721a))

# [0.110.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.109.2...v0.110.0) (2026-08-10)


### Features

* **quotes:** add 23 quotes to the corpus ([#128](https://github.com/Prog-Strength/prog-strength-api/issues/128)) ([31d993e](https://github.com/Prog-Strength/prog-strength-api/commit/31d993ef7fd2ba832961ed7ebafb6b4febec83d7))

## [0.109.2](https://github.com/Prog-Strength/prog-strength-api/compare/v0.109.1...v0.109.2) (2026-08-10)


### Bug Fixes

* **weather:** read history from /timeline/1h with start, not /timemachine ([#127](https://github.com/Prog-Strength/prog-strength-api/issues/127)) ([e68d1bf](https://github.com/Prog-Strength/prog-strength-api/commit/e68d1bf652ab5b46c8eb20023f83f7cf5079f368))

## [0.109.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.109.0...v0.109.1) (2026-08-10)


### Bug Fixes

* **weather:** ship the weather-backfill binary in the api image ([#126](https://github.com/Prog-Strength/prog-strength-api/issues/126)) ([9fd209f](https://github.com/Prog-Strength/prog-strength-api/commit/9fd209f161c3e251dc246248f44e4a631cd0ad2a)), closes [#125](https://github.com/Prog-Strength/prog-strength-api/issues/125)

# [0.109.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.108.1...v0.109.0) (2026-08-09)


### Features

* **weather:** capture activity conditions at import, with a paced backfill ([#125](https://github.com/Prog-Strength/prog-strength-api/issues/125)) ([188eea8](https://github.com/Prog-Strength/prog-strength-api/commit/188eea8eb8dceda370e4c1fa25755f487313a04c))

## [0.108.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.108.0...v0.108.1) (2026-08-09)


### Bug Fixes

* **weather:** call the One Call 4.0 /onecall paths and parse data[0] ([#124](https://github.com/Prog-Strength/prog-strength-api/issues/124)) ([80d2eeb](https://github.com/Prog-Strength/prog-strength-api/commit/80d2eeb2e378fd08b8db6b05be70aaea3a3dd0d9))

# [0.108.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.107.0...v0.108.0) (2026-08-09)


### Features

* **weather:** cache-first weather service with a durable daily call budget ([#123](https://github.com/Prog-Strength/prog-strength-api/issues/123)) ([241c988](https://github.com/Prog-Strength/prog-strength-api/commit/241c988205ab8a138f3cf7b6f88faf724e33571d))

# [0.107.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.106.0...v0.107.0) (2026-08-08)


### Features

* **dashboard:** sectioned layout contract ([#122](https://github.com/Prog-Strength/prog-strength-api/issues/122)) ([f27c8e1](https://github.com/Prog-Strength/prog-strength-api/commit/f27c8e17738417d70798f8843f48ba4a2d1fe9b8))

# [0.106.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.105.0...v0.106.0) (2026-08-08)


### Features

* **dashboard:** wikipedia links on the daily quote, and a reroll that survives a reload ([#121](https://github.com/Prog-Strength/prog-strength-api/issues/121)) ([d967a80](https://github.com/Prog-Strength/prog-strength-api/commit/d967a80ae62ff8766f0d41d2b1956585436fef98))

# [0.105.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.104.0...v0.105.0) (2026-08-08)


### Features

* **exercises:** add Machine Lat Pulldown to catalog ([#120](https://github.com/Prog-Strength/prog-strength-api/issues/120)) ([27067c7](https://github.com/Prog-Strength/prog-strength-api/commit/27067c7fc17a4c02dd7afc1edfbc121bd940769c))

# [0.104.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.103.0...v0.104.0) (2026-08-08)


### Features

* **dashboard:** daily quote tile served from an embedded corpus ([#119](https://github.com/Prog-Strength/prog-strength-api/issues/119)) ([f3416c3](https://github.com/Prog-Strength/prog-strength-api/commit/f3416c33273c60c93a382f691ab1aadfd7d09a5d))

# [0.103.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.102.0...v0.103.0) (2026-08-08)


### Features

* **calendar:** sync logged activities to google calendar ([#118](https://github.com/Prog-Strength/prog-strength-api/issues/118)) ([e6cdbd6](https://github.com/Prog-Strength/prog-strength-api/commit/e6cdbd6ad95fbd238879255650de00a9ec3ce57e))

# [0.102.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.101.2...v0.102.0) (2026-08-08)


### Features

* **memory:** carry source type and both provenance FKs on a search Match ([#117](https://github.com/Prog-Strength/prog-strength-api/issues/117)) ([f337ee3](https://github.com/Prog-Strength/prog-strength-api/commit/f337ee3736c2992e81ec2f33650f2d7a046293c9))

## [0.101.2](https://github.com/Prog-Strength/prog-strength-api/compare/v0.101.1...v0.101.2) (2026-08-06)


### Bug Fixes

* **whoop:** alert on a durable sync timestamp, not a per-process counter ([#115](https://github.com/Prog-Strength/prog-strength-api/issues/115)) ([6c71719](https://github.com/Prog-Strength/prog-strength-api/commit/6c7171947bed1dd371411ba0fed3b0a35bfda980))

## [0.101.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.101.0...v0.101.1) (2026-08-05)


### Bug Fixes

* **auth:** restore the mobile custom scheme in return_to_allowed_origins ([#114](https://github.com/Prog-Strength/prog-strength-api/issues/114)) ([4ceac89](https://github.com/Prog-Strength/prog-strength-api/commit/4ceac89bcb02a1bd1dbbc07d54f955ed1dff0a00)), closes [#53](https://github.com/Prog-Strength/prog-strength-api/issues/53)

# [0.101.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.100.0...v0.101.0) (2026-08-03)


### Features

* **dashboard:** running section payload, catalog ids, and family gating from dx/running-tile ([#113](https://github.com/Prog-Strength/prog-strength-api/issues/113)) ([0813e00](https://github.com/Prog-Strength/prog-strength-api/commit/0813e003836bf39c30d168de0008c3778b4d0934))

# [0.100.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.99.0...v0.100.0) (2026-08-03)


### Features

* **photos:** processing worker and pending reaper ([#110](https://github.com/Prog-Strength/prog-strength-api/issues/110)) ([4fab327](https://github.com/Prog-Strength/prog-strength-api/commit/4fab3272fbb05a445700e009f0b233db2248f76b))

# [0.99.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.98.0...v0.99.0) (2026-08-03)


### Features

* **photos:** reserve and commit endpoints for the two-phase upload ([#109](https://github.com/Prog-Strength/prog-strength-api/issues/109)) ([81535e7](https://github.com/Prog-Strength/prog-strength-api/commit/81535e78cb323e3b5f7c5160d4c3a5582cdadf8a))

# [0.98.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.97.0...v0.98.0) (2026-08-03)


### Features

* **photos:** schema and repository for the two-phase upload ([#107](https://github.com/Prog-Strength/prog-strength-api/issues/107)) ([db6f455](https://github.com/Prog-Strength/prog-strength-api/commit/db6f4555ad47008f45bfc52148e578318c12972c))

# [0.97.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.96.1...v0.97.0) (2026-08-03)


### Features

* **photos:** lossless JPEG metadata strip ([#106](https://github.com/Prog-Strength/prog-strength-api/issues/106)) ([b79b95f](https://github.com/Prog-Strength/prog-strength-api/commit/b79b95faf66a154f8adfbd863ed2d2148830cb46))

## [0.96.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.96.0...v0.96.1) (2026-08-02)


### Bug Fixes

* **upload:** lift the 10s request deadline on file-upload routes ([#104](https://github.com/Prog-Strength/prog-strength-api/issues/104)) ([bbd0ca7](https://github.com/Prog-Strength/prog-strength-api/commit/bbd0ca79de39e1ca1c39da95cc07889c32915776))

# [0.96.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.95.0...v0.96.0) (2026-08-02)


### Features

* **dashboard:** recovery-family tile ids and shared-section gating ([#103](https://github.com/Prog-Strength/prog-strength-api/issues/103)) ([9956811](https://github.com/Prog-Strength/prog-strength-api/commit/9956811623f2749f850c65b50ea6796c7b6723d8))

# [0.95.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.94.0...v0.95.0) (2026-08-02)


### Features

* **dashboard:** extend recovery summary payload with days, baselines, and HRV balance ([#102](https://github.com/Prog-Strength/prog-strength-api/issues/102)) ([fac9033](https://github.com/Prog-Strength/prog-strength-api/commit/fac903352a7ed7357fcd66623762caf548865087))

# [0.94.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.93.0...v0.94.0) (2026-08-01)


### Features

* **bloodpressure:** blood-pressure domain, API, and dashboard tile ([#101](https://github.com/Prog-Strength/prog-strength-api/issues/101)) ([6aa0f42](https://github.com/Prog-Strength/prog-strength-api/commit/6aa0f424414a6748da3f2da319e9aa546de8e0b9))

# [0.93.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.92.0...v0.93.0) (2026-08-01)


### Features

* **avatar:** make the upload ceiling a config knob and raise it to 5 MB ([#100](https://github.com/Prog-Strength/prog-strength-api/issues/100)) ([b962aa6](https://github.com/Prog-Strength/prog-strength-api/commit/b962aa648cb983db39e18152dd2bc425ca2068fc))

# [0.92.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.91.0...v0.92.0) (2026-08-01)


### Features

* **dashboard:** customizable tile layout + walking/cycling/hiking tiles ([#99](https://github.com/Prog-Strength/prog-strength-api/issues/99)) ([757fb8f](https://github.com/Prog-Strength/prog-strength-api/commit/757fb8f2355df0d1fd7765e8c47fc54f613605c0))

# [0.91.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.90.2...v0.91.0) (2026-07-31)


### Features

* **whoop:** admin diagnostics surface, resync, and ingestion instrumentation ([#98](https://github.com/Prog-Strength/prog-strength-api/issues/98)) ([7e1e37f](https://github.com/Prog-Strength/prog-strength-api/commit/7e1e37f2af146390ee130592405d88ce636a8d8b))

## [0.90.2](https://github.com/Prog-Strength/prog-strength-api/compare/v0.90.1...v0.90.2) (2026-07-31)


### Bug Fixes

* **activities:** resolve S3 credentials per presign, not once at startup ([#97](https://github.com/Prog-Strength/prog-strength-api/issues/97)) ([c6e1f70](https://github.com/Prog-Strength/prog-strength-api/commit/c6e1f70a2239376e34138d1dcd49982f3495079a))

## [0.90.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.90.0...v0.90.1) (2026-07-31)


### Bug Fixes

* **videos:** stop signing cache-control on the presigned upload ([#96](https://github.com/Prog-Strength/prog-strength-api/issues/96)) ([b3b9892](https://github.com/Prog-Strength/prog-strength-api/commit/b3b9892ac09fabf2c089d99e1677e07e8a6cf1e4))

# [0.90.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.89.0...v0.90.0) (2026-07-31)


### Features

* **activities:** attach videos to any activity ([#95](https://github.com/Prog-Strength/prog-strength-api/issues/95)) ([d916711](https://github.com/Prog-Strength/prog-strength-api/commit/d916711cb1006b344c1d3a657fed7d9faab186de))

# [0.89.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.88.0...v0.89.0) (2026-07-31)


### Features

* **photos:** store the full variant at native resolution, near-lossless ([#94](https://github.com/Prog-Strength/prog-strength-api/issues/94)) ([7fd804c](https://github.com/Prog-Strength/prog-strength-api/commit/7fd804c038b0fb0fd4e27926e52fabbf34c3de99))

# [0.88.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.87.0...v0.88.0) (2026-07-31)


### Features

* **activities:** compute heart-rate zones for every activity type ([#92](https://github.com/Prog-Strength/prog-strength-api/issues/92)) ([125c80e](https://github.com/Prog-Strength/prog-strength-api/commit/125c80e9a9bdb66ce582596ab866e816669d9f1a)), closes [Prog-Strength/prog-strength-web#131](https://github.com/Prog-Strength/prog-strength-web/issues/131)

# [0.87.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.86.1...v0.87.0) (2026-07-31)


### Features

* add activity photos (upload pipeline, S3 storage, feed hydration) ([#93](https://github.com/Prog-Strength/prog-strength-api/issues/93)) ([f30ed8f](https://github.com/Prog-Strength/prog-strength-api/commit/f30ed8f466b91e54b2124e2f0bbbee0e7363327b))

## [0.86.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.86.0...v0.86.1) (2026-07-31)


### Bug Fixes

* **timeline:** post every activity type to the social feed ([#91](https://github.com/Prog-Strength/prog-strength-api/issues/91)) ([a1c55bd](https://github.com/Prog-Strength/prog-strength-api/commit/a1c55bd6eb0316083c02e548a66020270a0d93fb)), closes [#90](https://github.com/Prog-Strength/prog-strength-api/issues/90)

# [0.86.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.85.0...v0.86.0) (2026-07-31)


### Features

* activity session notes — writable notes + endurance note distillation ([#89](https://github.com/Prog-Strength/prog-strength-api/issues/89)) ([30f9302](https://github.com/Prog-Strength/prog-strength-api/commit/30f930217246c1d90b3b17e36e7dc4e4ab49a546))

# [0.85.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.84.0...v0.85.0) (2026-07-30)


### Features

* **activities:** expose trackpoint position and grade on detail reads ([#87](https://github.com/Prog-Strength/prog-strength-api/issues/87)) ([df46939](https://github.com/Prog-Strength/prog-strength-api/commit/df469394394feabf7b522896a11bb048a01ed2bf))

# [0.84.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.83.2...v0.84.0) (2026-07-30)


### Features

* register hiking as a fifth endurance activity type ([#86](https://github.com/Prog-Strength/prog-strength-api/issues/86)) ([05ddd25](https://github.com/Prog-Strength/prog-strength-api/commit/05ddd2525b6b7dcecb41685e5e388a50ec44fd91))

## [0.83.2](https://github.com/Prog-Strength/prog-strength-api/compare/v0.83.1...v0.83.2) (2026-07-29)


### Bug Fixes

* **chat:** count messages in the listing query so GET /chat-sessions can't 500 ([#84](https://github.com/Prog-Strength/prog-strength-api/issues/84)) ([dcbe72f](https://github.com/Prog-Strength/prog-strength-api/commit/dcbe72f7edaca5dda4153fc4bcdad4358b0059a6)), closes [#77](https://github.com/Prog-Strength/prog-strength-api/issues/77) [#77](https://github.com/Prog-Strength/prog-strength-api/issues/77) [#77](https://github.com/Prog-Strength/prog-strength-api/issues/77)

## [0.83.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.83.0...v0.83.1) (2026-07-29)


### Bug Fixes

* stop vector memory distillation looping on units it can never distill ([#83](https://github.com/Prog-Strength/prog-strength-api/issues/83)) ([469f181](https://github.com/Prog-Strength/prog-strength-api/commit/469f1818e6c8a47a51d9763e351bdfb91b05cdd7)), closes [#78](https://github.com/Prog-Strength/prog-strength-api/issues/78) [#78](https://github.com/Prog-Strength/prog-strength-api/issues/78)

# [0.83.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.82.0...v0.83.0) (2026-07-29)


### Features

* remove /workouts shims and collapse completed_session_kind (stage 5) ([#81](https://github.com/Prog-Strength/prog-strength-api/issues/81)) ([#82](https://github.com/Prog-Strength/prog-strength-api/issues/82)) ([5683b98](https://github.com/Prog-Strength/prog-strength-api/commit/5683b981434c648159db29f1b97739ec692fdfd8))

# [0.82.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.81.0...v0.82.0) (2026-07-29)


### Features

* strength parity additions for unified surface ([#80](https://github.com/Prog-Strength/prog-strength-api/issues/80)) ([7751660](https://github.com/Prog-Strength/prog-strength-api/commit/7751660d13c0f5147ca7a4c8629e5e43ab7edbf7))

# [0.81.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.80.1...v0.81.0) (2026-07-28)


### Features

* unified activity model (stage 1 — API) ([#79](https://github.com/Prog-Strength/prog-strength-api/issues/79)) ([fc74298](https://github.com/Prog-Strength/prog-strength-api/commit/fc74298415323799e5622782904eeda858f9b534))

## [0.80.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.80.0...v0.80.1) (2026-07-23)


### Bug Fixes

* **whoop:** date recoveries by scored-at instant, not cycle start ([#76](https://github.com/Prog-Strength/prog-strength-api/issues/76)) ([8c83f55](https://github.com/Prog-Strength/prog-strength-api/commit/8c83f5566c9a59f36b28e4a552bf0e815ed35e56))

# [0.80.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.79.2...v0.80.0) (2026-07-23)


### Features

* **whoop:** structured observability for ingestion, webhooks, and token lifecycle ([#75](https://github.com/Prog-Strength/prog-strength-api/issues/75)) ([9612a8a](https://github.com/Prog-Strength/prog-strength-api/commit/9612a8a27143c851dd42dcb90e25535e47abb2ac))

## [0.79.2](https://github.com/Prog-Strength/prog-strength-api/compare/v0.79.1...v0.79.2) (2026-07-23)


### Bug Fixes

* **whoop:** request read:cycles scope so syncs can derive dates ([#74](https://github.com/Prog-Strength/prog-strength-api/issues/74)) ([4f6541e](https://github.com/Prog-Strength/prog-strength-api/commit/4f6541e2269fa67a0e4d10bbf0f494e112d9dbec))

## [0.79.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.79.0...v0.79.1) (2026-07-23)


### Bug Fixes

* **whoop:** add missing /developer prefix to WHOOP data API URLs ([#73](https://github.com/Prog-Strength/prog-strength-api/issues/73)) ([854a8ce](https://github.com/Prog-Strength/prog-strength-api/commit/854a8ce842902bc760b99c0b58ce64f08196fb27))

# [0.79.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.78.0...v0.79.0) (2026-07-23)


### Features

* **whoop:** recovery integration — oauth, webhook ingestion, reads ([#72](https://github.com/Prog-Strength/prog-strength-api/issues/72)) ([ed5c3cb](https://github.com/Prog-Strength/prog-strength-api/commit/ed5c3cbf0fab26852bc872b6598a96ebab21ee8f))

# [0.78.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.77.0...v0.78.0) (2026-07-17)


### Features

* **activity:** backfill route geometry for existing outdoor activities ([#71](https://github.com/Prog-Strength/prog-strength-api/issues/71)) ([85d7b19](https://github.com/Prog-Strength/prog-strength-api/commit/85d7b19e78a0b54a6c4bd3dbad9bc8b208bc07e7))

# [0.77.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.76.0...v0.77.0) (2026-07-17)


### Features

* **activity:** capture tcx gps route and expose on activity detail ([#70](https://github.com/Prog-Strength/prog-strength-api/issues/70)) ([4c34d59](https://github.com/Prog-Strength/prog-strength-api/commit/4c34d59ab90fbb6364d8f009724fb9bebf8dbdeb))

# [0.76.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.75.0...v0.76.0) (2026-07-15)


### Features

* **nutritionlookup:** add provider merge and candidate visibility to logs ([#69](https://github.com/Prog-Strength/prog-strength-api/issues/69)) ([c3fd06a](https://github.com/Prog-Strength/prog-strength-api/commit/c3fd06a21f1ed4f89c6e906078a27ce0f93712ad))

# [0.75.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.74.0...v0.75.0) (2026-07-06)


### Features

* **running:** max-effort estimation engine v2 with logged-best floor ([#68](https://github.com/Prog-Strength/prog-strength-api/issues/68)) ([f703e2e](https://github.com/Prog-Strength/prog-strength-api/commit/f703e2e9c7cccb07886a3e0a1ec796dc0256b529))

# [0.74.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.73.0...v0.74.0) (2026-07-03)


### Features

* **activity:** server-side running detail derivation + invariant gate ([#67](https://github.com/Prog-Strength/prog-strength-api/issues/67)) ([a7b43aa](https://github.com/Prog-Strength/prog-strength-api/commit/a7b43aa438b62bb9b62c13f7a27d3720f7aab3fe))

# [0.73.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.72.0...v0.73.0) (2026-07-03)


### Features

* **activity:** indoor tagging + distance calibration for treadmill runs ([#66](https://github.com/Prog-Strength/prog-strength-api/issues/66)) ([c3b726d](https://github.com/Prog-Strength/prog-strength-api/commit/c3b726d53dbb907f54d1757f601b554a0071e3a1))

# [0.72.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.71.0...v0.72.0) (2026-06-22)


### Features

* **deploy:** deploy via SSM Run Command; retire SSH deploy key ([#64](https://github.com/Prog-Strength/prog-strength-api/issues/64)) ([86dee29](https://github.com/Prog-Strength/prog-strength-api/commit/86dee298110b074a62be1549837745f0d20e674b))

# [0.71.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.70.0...v0.71.0) (2026-06-22)


### Features

* **vectormemory:** multi-source agent memory (workout-note ingestion) ([#63](https://github.com/Prog-Strength/prog-strength-api/issues/63)) ([97b23ef](https://github.com/Prog-Strength/prog-strength-api/commit/97b23ef6a336e0cddbca534ba43ab85e2900b900))

# [0.70.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.69.2...v0.70.0) (2026-06-22)


### Features

* **snapshot:** add GET /training-snapshot holistic training view ([#62](https://github.com/Prog-Strength/prog-strength-api/issues/62)) ([d9859b7](https://github.com/Prog-Strength/prog-strength-api/commit/d9859b7f42fc8c38bb069d6d052996487340f82a))

## [0.69.2](https://github.com/Prog-Strength/prog-strength-api/compare/v0.69.1...v0.69.2) (2026-06-22)


### Bug Fixes

* **auth:** allow apex origin as OAuth return_to target ([#61](https://github.com/Prog-Strength/prog-strength-api/issues/61)) ([f4b110b](https://github.com/Prog-Strength/prog-strength-api/commit/f4b110b8967275e06d9d09d3eef94ab06f177665)), closes [#53](https://github.com/Prog-Strength/prog-strength-api/issues/53)

## [0.69.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.69.0...v0.69.1) (2026-06-22)


### Bug Fixes

* **dashboard:** streak counts only completed activity ([#60](https://github.com/Prog-Strength/prog-strength-api/issues/60)) ([b14b6f0](https://github.com/Prog-Strength/prog-strength-api/commit/b14b6f0854e8efb558360ff83ca56f59cd07e347))

# [0.69.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.68.0...v0.69.0) (2026-06-22)


### Features

* **vectormemory:** instrument distillation goroutine with Prometheus metrics ([#59](https://github.com/Prog-Strength/prog-strength-api/issues/59)) ([b0ee215](https://github.com/Prog-Strength/prog-strength-api/commit/b0ee215f0994b543fa31cac576eb1092dbb01dde))

# [0.68.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.67.0...v0.68.0) (2026-06-22)


### Features

* **activity:** five-zone heart-rate breakdown on the running activity response ([#57](https://github.com/Prog-Strength/prog-strength-api/issues/57)) ([19b768f](https://github.com/Prog-Strength/prog-strength-api/commit/19b768fb64fe4b18ef268854f412875c77979b5f))

# [0.67.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.66.1...v0.67.0) (2026-06-21)


### Features

* **vectormemory:** set calibrated retrieval + dedup thresholds ([#56](https://github.com/Prog-Strength/prog-strength-api/issues/56)) ([3d699af](https://github.com/Prog-Strength/prog-strength-api/commit/3d699aff85345b9529e94a074f439b7c34aab4e6))

## [0.66.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.66.0...v0.66.1) (2026-06-21)


### Bug Fixes

* **build:** compile sqlite-vec against musl in the Docker builder ([#55](https://github.com/Prog-Strength/prog-strength-api/issues/55)) ([7832192](https://github.com/Prog-Strength/prog-strength-api/commit/7832192ed7f84aa834ec8dc110633fcea189b1a5))

# [0.66.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.65.0...v0.66.0) (2026-06-21)


### Features

* **vectormemory:** semantic agent memory over app.db via sqlite-vec ([#54](https://github.com/Prog-Strength/prog-strength-api/issues/54)) ([8146781](https://github.com/Prog-Strength/prog-strength-api/commit/81467818128e08e2edc20fccfd3ba4458d9ff9d1))

# [0.65.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.64.0...v0.65.0) (2026-06-20)


### Features

* **config:** centralize configuration in a committed config.toml ([#53](https://github.com/Prog-Strength/prog-strength-api/issues/53)) ([74defff](https://github.com/Prog-Strength/prog-strength-api/commit/74defff32fe00e8d6999b0aa5b345300e17f6181))

# [0.64.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.63.0...v0.64.0) (2026-06-20)


### Features

* **workout:** enrich strength workouts with Garmin TCX heart-rate data ([#52](https://github.com/Prog-Strength/prog-strength-api/issues/52)) ([844937b](https://github.com/Prog-Strength/prog-strength-api/commit/844937bc028c9eb922f219cb9ffe68861832ff3b))

# [0.63.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.62.0...v0.63.0) (2026-06-19)


### Features

* **dashboard:** add GET /dashboard/summary aggregate endpoint ([#51](https://github.com/Prog-Strength/prog-strength-api/issues/51)) ([73c5ced](https://github.com/Prog-Strength/prog-strength-api/commit/73c5ced665b8fa31961cf7ba68e2617719913e5d))

# [0.62.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.61.0...v0.62.0) (2026-06-19)


### Features

* **personal-records:** add recent_estimated_1rm_points to list response ([#50](https://github.com/Prog-Strength/prog-strength-api/issues/50)) ([ea7bead](https://github.com/Prog-Strength/prog-strength-api/commit/ea7beadccf000a83f73763b878c2a87c5d7e284b))

# [0.61.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.60.3...v0.61.0) (2026-06-18)


### Features

* **nutrition:** add best-effort POST /nutrition-log/batch ([#48](https://github.com/Prog-Strength/prog-strength-api/issues/48)) ([6bd1ee2](https://github.com/Prog-Strength/prog-strength-api/commit/6bd1ee2290caab84a31e297bdd18238cbbe6c8cf))

## [0.60.3](https://github.com/Prog-Strength/prog-strength-api/compare/v0.60.2...v0.60.3) (2026-06-17)


### Bug Fixes

* **planned-workouts:** render list results in the caller's timezone ([#47](https://github.com/Prog-Strength/prog-strength-api/issues/47)) ([97d5125](https://github.com/Prog-Strength/prog-strength-api/commit/97d5125981d2f4dac81b5b59cfa1924a94a899a2))

## [0.60.2](https://github.com/Prog-Strength/prog-strength-api/compare/v0.60.1...v0.60.2) (2026-06-17)


### Bug Fixes

* **planned-workouts:** render plan times in the plan's timezone ([#46](https://github.com/Prog-Strength/prog-strength-api/issues/46)) ([ba4d7b1](https://github.com/Prog-Strength/prog-strength-api/commit/ba4d7b197cd1edfc6ff923f952f4e3bab9bd6c93))

## [0.60.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.60.0...v0.60.1) (2026-06-17)


### Bug Fixes

* **planned-workouts:** list by user-local day + leveled slog logging ([#45](https://github.com/Prog-Strength/prog-strength-api/issues/45)) ([fca0574](https://github.com/Prog-Strength/prog-strength-api/commit/fca0574c47a077ae61b74876f80a25662ffa38d1))

# [0.60.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.59.1...v0.60.0) (2026-06-17)


### Features

* **planned-workouts:** log list calls with request_id for tracing ([#44](https://github.com/Prog-Strength/prog-strength-api/issues/44)) ([551db7e](https://github.com/Prog-Strength/prog-strength-api/commit/551db7ef82606501260e060fbd2f32c72094828d))

## [0.59.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.59.0...v0.59.1) (2026-06-16)


### Bug Fixes

* **auth:** allow single-* wildcard in return_to origin whitelist ([#43](https://github.com/Prog-Strength/prog-strength-api/issues/43)) ([573728b](https://github.com/Prog-Strength/prog-strength-api/commit/573728bdd63c61ecae9f8512b78e45477ca94961))

# [0.59.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.58.0...v0.59.0) (2026-06-16)


### Features

* **planned-workouts:** branded calendar event body + AMRAP support ([#42](https://github.com/Prog-Strength/prog-strength-api/issues/42)) ([ce61b0b](https://github.com/Prog-Strength/prog-strength-api/commit/ce61b0be0f22e4d4dfe46b1c4f39628398c986fc))

# [0.58.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.57.1...v0.58.0) (2026-06-16)


### Features

* **planned-workouts:** superset support on planned exercises ([#41](https://github.com/Prog-Strength/prog-strength-api/issues/41)) ([7db2d9a](https://github.com/Prog-Strength/prog-strength-api/commit/7db2d9aef1ccdd96102341e1c829db684c587900))

## [0.57.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.57.0...v0.57.1) (2026-06-16)


### Bug Fixes

* **planned-workouts:** sync Google event on edit; include notes + agenda ([#40](https://github.com/Prog-Strength/prog-strength-api/issues/40)) ([6f193b7](https://github.com/Prog-Strength/prog-strength-api/commit/6f193b79d3896884be57720ed02b3dfe715863d3))

# [0.57.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.56.0...v0.57.0) (2026-06-16)


### Features

* **social:** handle reachability, bio, profile stats, and timeline author identity ([#39](https://github.com/Prog-Strength/prog-strength-api/issues/39)) ([08f0fd9](https://github.com/Prog-Strength/prog-strength-api/commit/08f0fd9d2ffb841e65e20509bc295889152543e5))

# [0.56.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.55.0...v0.56.0) (2026-06-16)


### Bug Fixes

* **release:** push changelog to protected main via an admin bypass token ([#38](https://github.com/Prog-Strength/prog-strength-api/issues/38)) ([77f10d7](https://github.com/Prog-Strength/prog-strength-api/commit/77f10d783ba3d54fbe508ba75f12549db4b5b11c))


### Features

* **cors:** allow multiple origins incl. a wildcard for Vercel previews ([#37](https://github.com/Prog-Strength/prog-strength-api/issues/37)) ([8f15757](https://github.com/Prog-Strength/prog-strength-api/commit/8f1575712fd97e11e73e72d4c26179bfafdd8c97))

# [0.55.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.54.0...v0.55.0) (2026-06-15)


### Features

* **planned-workouts:** auto-reconcile logged sessions with planned workouts ([#36](https://github.com/Prog-Strength/prog-strength-api/issues/36)) ([30a6d2a](https://github.com/Prog-Strength/prog-strength-api/commit/30a6d2a190c0b225972a3abef0d2021bc9047bfd))

# [0.54.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.53.1...v0.54.0) (2026-06-15)


### Features

* **planned-workouts:** support run activity kind alongside lift ([#35](https://github.com/Prog-Strength/prog-strength-api/issues/35)) ([dc65c8a](https://github.com/Prog-Strength/prog-strength-api/commit/dc65c8a851b8a77d3b743e0510150d0d773e95a4))

## [0.53.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.53.0...v0.53.1) (2026-06-15)


### Bug Fixes

* **auth:** accept auth_token cookie as bearer-token fallback in RequireUser ([#34](https://github.com/Prog-Strength/prog-strength-api/issues/34)) ([0e65a53](https://github.com/Prog-Strength/prog-strength-api/commit/0e65a534437e72e40df492f2cc363170a52b81c8))

# [0.53.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.52.0...v0.53.0) (2026-06-15)


### Features

* planned workouts + Google Calendar sync (API) ([#33](https://github.com/Prog-Strength/prog-strength-api/issues/33)) ([8fbd074](https://github.com/Prog-Strength/prog-strength-api/commit/8fbd0747253171303b63507d6891b036977a1762))

# [0.52.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.51.0...v0.52.0) (2026-06-15)


### Features

* **social:** username, follow graph, discovery, and timeline fan-out ([#32](https://github.com/Prog-Strength/prog-strength-api/issues/32)) ([4a33878](https://github.com/Prog-Strength/prog-strength-api/commit/4a33878d035730ee3b888c3b56daad0ef8624a76))

# [0.51.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.50.0...v0.51.0) (2026-06-15)


### Features

* **beta:** dynamic DB-backed beta allowlist with admin endpoints ([#31](https://github.com/Prog-Strength/prog-strength-api/issues/31)) ([0ccac90](https://github.com/Prog-Strength/prog-strength-api/commit/0ccac90b7a2f34178ae03c3d09cbfe64891ad659))

# [0.50.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.49.0...v0.50.0) (2026-06-15)


### Features

* **timeline:** user timeline feed domain, endpoints, and backfill ([#30](https://github.com/Prog-Strength/prog-strength-api/issues/30)) ([f0effee](https://github.com/Prog-Strength/prog-strength-api/commit/f0effee8b7e7e092c5125e7765c38c8d7561befc))

# [0.49.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.48.0...v0.49.0) (2026-06-15)


### Features

* **steps:** daily steps domain, upsert-by-date endpoints + goal ([#29](https://github.com/Prog-Strength/prog-strength-api/issues/29)) ([5e31c28](https://github.com/Prog-Strength/prog-strength-api/commit/5e31c28cc5e1e3628819bf91d7e4dda6a8b11616))

# [0.48.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.47.1...v0.48.0) (2026-06-15)


### Features

* **running:** max-effort estimate engine + endpoints ([#28](https://github.com/Prog-Strength/prog-strength-api/issues/28)) ([36057ec](https://github.com/Prog-Strength/prog-strength-api/commit/36057ecd6a2a513d8d6755b2a5b3801ea8e87ba8))

## [0.47.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.47.0...v0.47.1) (2026-06-12)


### Bug Fixes

* **ci:** authenticate to AWS via the shared OIDC role ([#27](https://github.com/Prog-Strength/prog-strength-api/issues/27)) ([c2590d9](https://github.com/Prog-Strength/prog-strength-api/commit/c2590d98330ee3c38ca3f768488ab651ab8178a7))

# [0.47.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.46.0...v0.47.0) (2026-06-12)


### Features

* Prometheus metrics for the nutrition lookup service ([#26](https://github.com/Prog-Strength/prog-strength-api/issues/26)) ([28f97d4](https://github.com/Prog-Strength/prog-strength-api/commit/28f97d4d8dd70f33956bbdeaa2fbdc3b5164eb35))

# [0.46.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.45.0...v0.46.0) (2026-06-12)


### Features

* request-correlated structured logging for nutrition lookup ([#24](https://github.com/Prog-Strength/prog-strength-api/issues/24)) ([5b4a17f](https://github.com/Prog-Strength/prog-strength-api/commit/5b4a17f7c889c7ef48c2acfb49ff055ed92f5bdb))

# [0.45.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.44.1...v0.45.0) (2026-06-12)


### Features

* nutrition lookup endpoint with FatSecret + USDA and durable cache ([#22](https://github.com/Prog-Strength/prog-strength-api/issues/22)) ([3b71c7e](https://github.com/Prog-Strength/prog-strength-api/commit/3b71c7ebd334a4a7ace6f56d10193b31f7b19cbf))

## [0.44.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.44.0...v0.44.1) (2026-06-10)


### Bug Fixes

* **deploy:** thread AVATAR_BUCKET_NAME through release + manual-deploy ([#21](https://github.com/Prog-Strength/prog-strength-api/issues/21)) ([dd3730f](https://github.com/Prog-Strength/prog-strength-api/commit/dd3730fdebc8f2bfe06e0739ea9ac8a7cde9f57c))

# [0.44.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.43.0...v0.44.0) (2026-06-10)


### Features

* **workout:** per-exercise trends + movement-pattern filter for progression ([#20](https://github.com/Prog-Strength/prog-strength-api/issues/20)) ([cf0e4e2](https://github.com/Prog-Strength/prog-strength-api/commit/cf0e4e255370d1a8a0959dc5a8735ca80dc36580))

# [0.43.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.42.0...v0.43.0) (2026-06-10)


### Features

* **profile:** editable display_name + height_cm + avatar via resolved /me ([#19](https://github.com/Prog-Strength/prog-strength-api/issues/19)) ([4dc3e59](https://github.com/Prog-Strength/prog-strength-api/commit/4dc3e591a74ba192aa236a06532716ac9cb60acf)), closes [#18](https://github.com/Prog-Strength/prog-strength-api/issues/18) [#18](https://github.com/Prog-Strength/prog-strength-api/issues/18)

# [0.42.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.41.1...v0.42.0) (2026-06-10)


### Features

* **activity:** running best efforts — /running/best-efforts, history endpoints, backfill ([#18](https://github.com/Prog-Strength/prog-strength-api/issues/18)) ([4c9a8f3](https://github.com/Prog-Strength/prog-strength-api/commit/4c9a8f3087c33adeaf169b4e52f091937b80bf26))

## [0.41.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.41.0...v0.41.1) (2026-06-09)


### Bug Fixes

* **usage:** default to hardcoded price table when env override is unset ([#17](https://github.com/Prog-Strength/prog-strength-api/issues/17)) ([69e971e](https://github.com/Prog-Strength/prog-strength-api/commit/69e971e1db44f978c2ca104eed79f9de82efda69))

# [0.41.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.40.0...v0.41.0) (2026-06-09)


### Features

* **usage:** per-user daily usage cap — GET /me/usage + speak telemetry ([#16](https://github.com/Prog-Strength/prog-strength-api/issues/16)) ([ef2c610](https://github.com/Prog-Strength/prog-strength-api/commit/ef2c61062b52b50d7d1a9aca12c6ed65cb31839e))

# [0.40.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.39.0...v0.40.0) (2026-06-09)


### Features

* **activity:** generalize running domain into sport-agnostic activity ingest ([#15](https://github.com/Prog-Strength/prog-strength-api/issues/15)) ([ddad7a3](https://github.com/Prog-Strength/prog-strength-api/commit/ddad7a34649f0f68ed0bbad1410a7e6de397747d))

# [0.39.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.38.2...v0.39.0) (2026-06-09)


### Features

* **api:** per-request id for response correlation + storage_failed log fix ([#13](https://github.com/Prog-Strength/prog-strength-api/issues/13)) ([e19fd78](https://github.com/Prog-Strength/prog-strength-api/commit/e19fd7858402ea8ae2b8b7130ff4a8f7d28db633))

## [0.38.2](https://github.com/Prog-Strength/prog-strength-api/compare/v0.38.1...v0.38.2) (2026-06-07)


### Bug Fixes

* **deploy:** forward TCX_BUCKET_NAME into prod .env ([#12](https://github.com/Prog-Strength/prog-strength-api/issues/12)) ([a76253c](https://github.com/Prog-Strength/prog-strength-api/commit/a76253cb8e4ded195fc32ca52e503f8bd2d67fbb))

## [0.38.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.38.0...v0.38.1) (2026-06-06)


### Bug Fixes

* **running:** allow re-import of a deleted run ([#11](https://github.com/Prog-Strength/prog-strength-api/issues/11)) ([c31d224](https://github.com/Prog-Strength/prog-strength-api/commit/c31d22466e32b0360bca03408722bd5057f2897a))

# [0.38.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.37.0...v0.38.0) (2026-06-06)


### Features

* **running:** pace outlier filter + date-range listing ([#10](https://github.com/Prog-Strength/prog-strength-api/issues/10)) ([cda4174](https://github.com/Prog-Strength/prog-strength-api/commit/cda41742bf9ff73a7c97324286ea6220a1ee2a1f))

# [0.37.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.36.0...v0.37.0) (2026-06-06)


### Features

* **running:** Garmin TCX import, sessions, metrics (migration 013) ([#9](https://github.com/Prog-Strength/prog-strength-api/issues/9)) ([43e93bc](https://github.com/Prog-Strength/prog-strength-api/commit/43e93bcf903011f12245aef83f9728b5d4031783))

# [0.36.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.35.0...v0.36.0) (2026-06-06)


### Features

* **api:** record had_image telemetry on agent turns ([#7](https://github.com/Prog-Strength/prog-strength-api/issues/7)) ([94c5aa1](https://github.com/Prog-Strength/prog-strength-api/commit/94c5aa1481e83d52943f40be31c66cb334ac039e))

# [0.35.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.34.0...v0.35.0) (2026-06-06)


### Features

* **api:** custom meal log entries (migration 012 + endpoint + PUT extension) ([#6](https://github.com/Prog-Strength/prog-strength-api/issues/6)) ([6ee9090](https://github.com/Prog-Strength/prog-strength-api/commit/6ee9090a18eeb9172f0a90883b5084fd36655584))

# [0.34.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.33.1...v0.34.0) (2026-06-04)


### Features

* **api:** bodyweight goal endpoints + PUT /bodyweight/{id} ([#5](https://github.com/Prog-Strength/prog-strength-api/issues/5)) ([f8c7a00](https://github.com/Prog-Strength/prog-strength-api/commit/f8c7a00e5974b77433d9c2ba6b38df32c70c12fb))

## [0.33.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.33.0...v0.33.1) (2026-06-03)


### Bug Fixes

* **api:** wire timezone date contract into handler + repository ([#4](https://github.com/Prog-Strength/prog-strength-api/issues/4)) ([d3e4f01](https://github.com/Prog-Strength/prog-strength-api/commit/d3e4f01c27d20998b07519b098320d5642a6c16a)), closes [#3](https://github.com/Prog-Strength/prog-strength-api/issues/3) [#3](https://github.com/Prog-Strength/prog-strength-api/issues/3)

# [0.33.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.32.0...v0.33.0) (2026-06-03)


### Features

* **api:** timezone-aware nutrition date contract + local-day aggregation ([#3](https://github.com/Prog-Strength/prog-strength-api/issues/3)) ([2fe7efb](https://github.com/Prog-Strength/prog-strength-api/commit/2fe7efb744d5add0e3974896dc659ca1615ca522))

# [0.32.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.31.0...v0.32.0) (2026-06-03)


### Features

* **api:** add GET /me endpoint returning the authed user ([#2](https://github.com/Prog-Strength/prog-strength-api/issues/2)) ([62eaee2](https://github.com/Prog-Strength/prog-strength-api/commit/62eaee2be00639b7d9b371e4a919cc5d02ed6b39))

# [0.31.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.30.1...v0.31.0) (2026-06-02)


### Features

* **api:** intent classification persistence + internal endpoint ([#1](https://github.com/Prog-Strength/prog-strength-api/issues/1)) ([abc6b8e](https://github.com/Prog-Strength/prog-strength-api/commit/abc6b8ed8fd6d9799756e4b5fc111b1f3f5f64fb))

## [0.30.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.30.0...v0.30.1) (2026-05-31)


### Bug Fixes

* **deploy:** write AWS_REGION to .env so awslogs driver finds the right region ([5e1b39d](https://github.com/Prog-Strength/prog-strength-api/commit/5e1b39d67f32587e25ec1c38efb48e621207a789))

# [0.30.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.29.1...v0.30.0) (2026-05-31)


### Features

* **nutrition:** per-user daily macro goals API ([2beb0f3](https://github.com/Prog-Strength/prog-strength-api/commit/2beb0f36067cdea0eff9d15cfe9bfba84d5d2005))

## [0.29.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.29.0...v0.29.1) (2026-05-30)


### Bug Fixes

* **cors:** allow PATCH so chat title updates land from the web ([625dc91](https://github.com/Prog-Strength/prog-strength-api/commit/625dc91eaeba202d5ef459ee787adad2e0f6be50))

# [0.29.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.28.4...v0.29.0) (2026-05-30)


### Features

* **chat:** persistent chat sessions API (Phase 1) ([d1e8db5](https://github.com/Prog-Strength/prog-strength-api/commit/d1e8db5b5c02363c7e7770bbfc7d2d2acdfa02a9))

## [0.28.4](https://github.com/Prog-Strength/prog-strength-api/compare/v0.28.3...v0.28.4) (2026-05-30)


### Performance Improvements

* **workout,exercise:** batched IN-clause hydration ([fe8877e](https://github.com/Prog-Strength/prog-strength-api/commit/fe8877e25898c52f5ac7e1ff274a62009d37b190))

## [0.28.3](https://github.com/Prog-Strength/prog-strength-api/compare/v0.28.2...v0.28.3) (2026-05-30)


### Bug Fixes

* **exercise:** drain rows before nested queries to avoid SQL pool deadlock ([c60f94b](https://github.com/Prog-Strength/prog-strength-api/commit/c60f94b077a3846c6add42e6d4c089ae8e5f81c6))

## [0.28.2](https://github.com/Prog-Strength/prog-strength-api/compare/v0.28.1...v0.28.2) (2026-05-30)


### Bug Fixes

* **workout:** drain rows before nested queries to avoid SQL pool deadlock ([0bf5684](https://github.com/Prog-Strength/prog-strength-api/commit/0bf568457c3aeb02b4ef44d514495ddfe9b5fb6b))

## [0.28.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.28.0...v0.28.1) (2026-05-30)


### Bug Fixes

* **auth:** allow custom-scheme return_to URLs (mobile deep links) ([a64fa11](https://github.com/Prog-Strength/prog-strength-api/commit/a64fa11631999619b1bd23b06680fe8bb03d7d3b))

# [0.28.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.27.0...v0.28.0) (2026-05-30)


### Features

* **nutrition:** categorize log entries by meal ([6886e31](https://github.com/Prog-Strength/prog-strength-api/commit/6886e313703ff84d1a9aaef01435dddaad321077))

# [0.27.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.26.0...v0.27.0) (2026-05-30)


### Features

* **bodyweight:** scale-reading log + trend endpoints (Phase 3) ([691390f](https://github.com/Prog-Strength/prog-strength-api/commit/691390f6eadb569ada63555706bb694e20ffa164))

# [0.26.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.25.0...v0.26.0) (2026-05-30)


### Features

* **nutrition:** recipes + recipe-backed log entries (Phase 2) ([d12546a](https://github.com/Prog-Strength/prog-strength-api/commit/d12546a7ddaeced147274e2f6f0ededc06bf4bf5))

# [0.25.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.24.0...v0.25.0) (2026-05-30)


### Features

* **nutrition:** pantry items + daily nutrition log (Phase 1) ([283ea53](https://github.com/Prog-Strength/prog-strength-api/commit/283ea53d065a34b92df3252f58af08d0e088bf0a))

# [0.24.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.23.1...v0.24.0) (2026-05-21)


### Features

* **headline-exercises:** per-user customization of Personal Records lineup ([b6ac1f9](https://github.com/Prog-Strength/prog-strength-api/commit/b6ac1f9723015db12cb57978a27b34bbd45aa125))

## [0.23.1](https://github.com/Prog-Strength/prog-strength-api/compare/v0.23.0...v0.23.1) (2026-05-19)


### Bug Fixes

* **build:** build image on a native ARM runner ([326b9a8](https://github.com/Prog-Strength/prog-strength-api/commit/326b9a88578997b20904f77f0f7c46acce2c622d))

# [0.23.0](https://github.com/Prog-Strength/prog-strength-api/compare/v0.22.0...v0.23.0) (2026-05-19)


### Features

* **build:** publish API image to ECR and pull from it on deploy ([6ee11fa](https://github.com/Prog-Strength/prog-strength-api/commit/6ee11fa7de49d20a58aedf802db1d9a1f70c3dd5))

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
