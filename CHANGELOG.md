# Changelog

## [0.2.0](https://github.com/fclairamb/solidping/compare/v0.1.0...v0.2.0) (2026-07-04)


### Bug Fixes

* **deps:** update dependency recharts to v3.9.2 ([#97](https://github.com/fclairamb/solidping/issues/97)) ([c7917cc](https://github.com/fclairamb/solidping/commit/c7917ccd891e0e1c54d879183982333d1e8cceb4))
* **deps:** update github.com/dop251/goja digest to b07b744 ([#90](https://github.com/fclairamb/solidping/issues/90)) ([3f33ee6](https://github.com/fclairamb/solidping/commit/3f33ee65bb13b0f3b81c8ddb72e347b311f8b0cc))
* **deps:** update go dependencies (non-major) ([#92](https://github.com/fclairamb/solidping/issues/92)) ([138e3ae](https://github.com/fclairamb/solidping/commit/138e3ae58b99630331adf7033fe672a09268b7c3))


### Miscellaneous Chores

* retry release-as trailer for v0.2.0 ([#98](https://github.com/fclairamb/solidping/issues/98)) ([fb8e011](https://github.com/fclairamb/solidping/commit/fb8e0112f28833e53c52e5278634b6fbe01d1ce6))

## [0.1.0](https://github.com/fclairamb/solidping/compare/v0.0.0...v0.1.0) (2026-06-22)


### Features

* add check type registry, sample configs, notification senders, and observability integrations ([eea049e](https://github.com/fclairamb/solidping/commit/eea049e59f998cc0247110fc86d2617b31aedff8))
* batch — Warning/Degraded statuses, SSL/Docker check upgrades, MCP OAuth 2.1, config-as-code ([#85](https://github.com/fclairamb/solidping/issues/85)) ([936d830](https://github.com/fclairamb/solidping/commit/936d8304af930542c7a341353eb08a7708aa668a))
* batch spec implementations (dashboard UI, jobs, notifications, SaaS, perf) ([#78](https://github.com/fclairamb/solidping/issues/78)) ([fe003e4](https://github.com/fclairamb/solidping/commit/fe003e4f86119af8a3e9425949b34966422d1d85))
* bulk feature integration (discovery, web push, badges, webhooks, and more) ([#73](https://github.com/fclairamb/solidping/issues/73)) ([a976856](https://github.com/fclairamb/solidping/commit/a976856d541d7216e7172486a8cc89cba0a5759d))
* custom user-agent, refresh checks, vite plugin switch, CI updates ([#1](https://github.com/fclairamb/solidping/issues/1)) ([cc00858](https://github.com/fclairamb/solidping/commit/cc00858249a731c46e96d2105d3d4c9e49674656))
* documentation site served at /docs (co-located in web/docs) ([#88](https://github.com/fclairamb/solidping/issues/88)) ([27d9ccd](https://github.com/fclairamb/solidping/commit/27d9ccd4ec933979a5c788be5fdde44f177c61e7))
* email check frontend (spec 03) ([#30](https://github.com/fclairamb/solidping/issues/30)) ([aebd106](https://github.com/fclairamb/solidping/commit/aebd1062b8a64d7bbc1ef1c99ba332a664432c80))
* email inbox foundation via JMAP (spec 01) ([#24](https://github.com/fclairamb/solidping/issues/24)) ([0f3b0c1](https://github.com/fclairamb/solidping/commit/0f3b0c1a777ed65b84a28550ab5b9f30743b7fa4))
* email passive checks (spec 02) ([#27](https://github.com/fclairamb/solidping/issues/27)) ([ba5536f](https://github.com/fclairamb/solidping/commit/ba5536f540257dfec2571ce39a39b0fdabe1de53))
* **entitlements:** trim to MaxSSOUsers + MaxChecksPerMinute + status page history fixes ([#50](https://github.com/fclairamb/solidping/issues/50)) ([a007de0](https://github.com/fclairamb/solidping/commit/a007de0fc3774b209fa35e5e5d1693e63d7f0b4b))
* **entitlements:** trim to MaxSSOUsers + MaxChecksPerMinute, enforce both ([#49](https://github.com/fclairamb/solidping/issues/49)) ([d25cc5f](https://github.com/fclairamb/solidping/commit/d25cc5fe794669115c03ab5fe37f4890d9c0aeae))
* group incident correlation (spec 04 backend v1) ([#31](https://github.com/fclairamb/solidping/issues/31)) ([1883986](https://github.com/fclairamb/solidping/commit/18839861b23250d74cd4abe3e64d7c1a67c8f617))
* **observability:** instrument hot path + add bench harness ([#60](https://github.com/fclairamb/solidping/issues/60)) ([c6604cc](https://github.com/fclairamb/solidping/commit/c6604cc1dbe72b6447c7240a23f22a7211d04b13))
* **oncall:** searchable timezone dropdown + sidebar logo polish ([#46](https://github.com/fclairamb/solidping/issues/46)) ([4396898](https://github.com/fclairamb/solidping/commit/4396898f09d40daf9a841dd7b92c18b922fd0008))
* per-IP HTTP rate limiting and concurrency limiting ([#59](https://github.com/fclairamb/solidping/issues/59)) ([f9cc273](https://github.com/fclairamb/solidping/commit/f9cc2736e55fe7f0fbd8673a9652e63df4cd1b80))
* soften HTTP rate limits with bounded queues + request timeout ([#63](https://github.com/fclairamb/solidping/issues/63)) ([025501b](https://github.com/fclairamb/solidping/commit/025501bc874b3c903b6d5b1f101739cf23c2958f))
* SolidPing — distributed uptime monitoring platform ([eef4383](https://github.com/fclairamb/solidping/commit/eef4383fdeff1219159714db70510b7b6c8067b0))
* **statuspages:** drag-and-drop resource reordering + dash conventions ([#41](https://github.com/fclairamb/solidping/issues/41)) ([cf910d4](https://github.com/fclairamb/solidping/commit/cf910d469e239ca02172a9f18fe01a065fdf626f))


### Bug Fixes

* **deps:** update dependency i18next to v26 ([#26](https://github.com/fclairamb/solidping/issues/26)) ([d5d1b7e](https://github.com/fclairamb/solidping/commit/d5d1b7edceec47fdc51eec2f4bad16e138906108))
* **deps:** update dependency lucide-react to v1 ([#28](https://github.com/fclairamb/solidping/issues/28)) ([cdc8e8c](https://github.com/fclairamb/solidping/commit/cdc8e8cc02c18240100586378b3a122d360444d6))
* **deps:** update dependency react-i18next to v17 ([#29](https://github.com/fclairamb/solidping/issues/29)) ([13135d8](https://github.com/fclairamb/solidping/commit/13135d85bc9f00f7c46220f1b64b08b9ff5d238e))
* **deps:** update dependency recharts to v3.8.1 ([#14](https://github.com/fclairamb/solidping/issues/14)) ([5d95a3b](https://github.com/fclairamb/solidping/commit/5d95a3b99d6de02adae3aeaf7b052fd242c0f114))
* **deps:** update go dependencies (non-major) ([#16](https://github.com/fclairamb/solidping/issues/16)) ([2450f13](https://github.com/fclairamb/solidping/commit/2450f1368d251bc5d74e8c2978145c9da53efef7))
* **deps:** update go dependencies (non-major) ([#19](https://github.com/fclairamb/solidping/issues/19)) ([f842310](https://github.com/fclairamb/solidping/commit/f842310dc631d7461b3e26108ea569bb4b2b8795))
* **deps:** update go dependencies (non-major) ([#37](https://github.com/fclairamb/solidping/issues/37)) ([c020eca](https://github.com/fclairamb/solidping/commit/c020ecaefdb0676eca0b9961c19cbe6f633a40e6))
* **deps:** update go dependencies (non-major) ([#44](https://github.com/fclairamb/solidping/issues/44)) ([ff0bcd0](https://github.com/fclairamb/solidping/commit/ff0bcd0026007fd86c33222aeddaea2f329f7540))
* **deps:** update go dependencies (non-major) ([#52](https://github.com/fclairamb/solidping/issues/52)) ([2e3bc28](https://github.com/fclairamb/solidping/commit/2e3bc2845ffdabdd77db9994d48addb1f91f8e32))
* **deps:** update go dependencies (non-major) ([#69](https://github.com/fclairamb/solidping/issues/69)) ([a65ed4c](https://github.com/fclairamb/solidping/commit/a65ed4ca10bf1e6a0bcfb09f81e43274a424d2b6))
* **deps:** update go dependencies (non-major) ([#77](https://github.com/fclairamb/solidping/issues/77)) ([a0920ff](https://github.com/fclairamb/solidping/commit/a0920ffbe3dc38931acb120d62af4d6f441a3d03))
* **deps:** update go dependencies (non-major) to v1.4.2 ([#86](https://github.com/fclairamb/solidping/issues/86)) ([1eb7504](https://github.com/fclairamb/solidping/commit/1eb7504a2d1917183708ffe1b74fe010ef63d329))
* **deps:** update module github.com/aws/aws-sdk-go-v2/config to v1.32.18 ([#72](https://github.com/fclairamb/solidping/issues/72)) ([6406537](https://github.com/fclairamb/solidping/commit/64065376412136a54afc3f7542db0212c13bb833))
* **deps:** update module github.com/aws/aws-sdk-go-v2/service/s3 to v1.101.0 ([#39](https://github.com/fclairamb/solidping/issues/39)) ([f9c9563](https://github.com/fclairamb/solidping/commit/f9c956369d4c928a849b8bdc59f7340d636ad647))
* **deps:** update module github.com/go-webauthn/webauthn to v0.17.3 ([#45](https://github.com/fclairamb/solidping/issues/45)) ([593931a](https://github.com/fclairamb/solidping/commit/593931a9d40999d9225722dbe67b5b237db48427))
* **deps:** update module github.com/go-webauthn/webauthn to v0.17.4 ([#70](https://github.com/fclairamb/solidping/issues/70)) ([fca1c97](https://github.com/fclairamb/solidping/commit/fca1c9783398e4a7e951d5a8fa72930c675b8f08))
* **deps:** update module github.com/ibm/sarama to v1.48.1 ([#47](https://github.com/fclairamb/solidping/issues/47)) ([93ccf3a](https://github.com/fclairamb/solidping/commit/93ccf3aa1e06d6a1fe25b4557d28a750fbcd2a36))
* **deps:** update module github.com/ibm/sarama to v1.48.2 ([#54](https://github.com/fclairamb/solidping/issues/54)) ([e84d868](https://github.com/fclairamb/solidping/commit/e84d868829a3304b2f5ff187b117a5ad6a4dec09))
* **deps:** update module github.com/ibm/sarama to v1.49.0 ([#64](https://github.com/fclairamb/solidping/issues/64)) ([fd61bd7](https://github.com/fclairamb/solidping/commit/fd61bd74497828de9057da68215faa8d1323dd14))
* **deps:** update module github.com/oapi-codegen/runtime to v1.4.1 ([#65](https://github.com/fclairamb/solidping/issues/65)) ([a3a3e9f](https://github.com/fclairamb/solidping/commit/a3a3e9f378d8768e56838dd15276cd7468d3e1db))
* **deps:** update module github.com/prometheus/common to v0.67.5 ([#61](https://github.com/fclairamb/solidping/issues/61)) ([095dd6f](https://github.com/fclairamb/solidping/commit/095dd6fda83f4d1d7ad51bf4e84b19eb9ff2d9fc))
* **deps:** update module github.com/slack-go/slack to v0.24.0 ([#75](https://github.com/fclairamb/solidping/issues/75)) ([6e86d5b](https://github.com/fclairamb/solidping/commit/6e86d5b6307bd0b995c0c0153106435cbd5070e7))
* **deps:** update module golang.org/x/sys to v0.44.0 ([#42](https://github.com/fclairamb/solidping/issues/42)) ([a245730](https://github.com/fclairamb/solidping/commit/a24573052dfdfd6d02b6db5276ba6ae43e01013f))
* **deps:** update module golang.org/x/sys to v0.45.0 ([#67](https://github.com/fclairamb/solidping/issues/67)) ([49c3d90](https://github.com/fclairamb/solidping/commit/49c3d902f7eca083c02437bff813dfd4ae5c7712))
* **deps:** update module golang.org/x/term to v0.43.0 ([#43](https://github.com/fclairamb/solidping/issues/43)) ([4a934f1](https://github.com/fclairamb/solidping/commit/4a934f1448e700ed63c06443e34700ecd1baf724))
* **deps:** update module google.golang.org/grpc to v1.81.0 ([#36](https://github.com/fclairamb/solidping/issues/36)) ([1be183a](https://github.com/fclairamb/solidping/commit/1be183a4860016fa297ae5dba586868d67bc713b))
* **deps:** update module google.golang.org/grpc to v1.81.1 ([#55](https://github.com/fclairamb/solidping/issues/55)) ([6bfac2d](https://github.com/fclairamb/solidping/commit/6bfac2d3e345eff4bf80cacedeff0fe3ac74a36e))
* status lifecycle improvements and created/running result handling ([#5](https://github.com/fclairamb/solidping/issues/5)) ([fc64c7d](https://github.com/fclairamb/solidping/commit/fc64c7d87c25f2b6be0f9722f46e024b2c64ca1b))
* **status0:** white page header and de-duplicated footer ([#48](https://github.com/fclairamb/solidping/issues/48)) ([77506a7](https://github.com/fclairamb/solidping/commit/77506a7533d9e0fc99ffc6dcaef33f2c21a9ac6a))
