# Changelog

## [0.2.0](https://github.com/rknightion/codexlb2otel/compare/v0.1.0...v0.2.0) (2026-08-09)


### Features

* **archive:** delete yesterday and older, from inside the container ([f937dee](https://github.com/rknightion/codexlb2otel/commit/f937deebea4581cb5e0940845318ba019695d1a6)), closes [#28](https://github.com/rknightion/codexlb2otel/issues/28)
* **clbsum:** float to the latest DeepSeek, set reasoning effort, and stop paying twice ([419499b](https://github.com/rknightion/codexlb2otel/commit/419499b23b21bdc81e48d0977eccd55cc069f9aa)), closes [#38](https://github.com/rknightion/codexlb2otel/issues/38)
* **clbsum:** summarise what work agent sessions actually accomplished ([84a6ca4](https://github.com/rknightion/codexlb2otel/commit/84a6ca4b29db751f666ea96513068872bfa0d0bb)), closes [#37](https://github.com/rknightion/codexlb2otel/issues/37)
* **dashboards:** one v2 dashboard covering every signal, generated and coverage-checked ([1a4ded3](https://github.com/rknightion/codexlb2otel/commit/1a4ded33133b95b1a1abb42cde42207332340dc5))
* **deploy:** enable the sigil sink and make the compose fragment actually runnable ([c06983e](https://github.com/rknightion/codexlb2otel/commit/c06983ed84fda580a518b4a640afd675bdc58792))
* **live:** adaptive retention, stalled agents, collapse and deep links ([e1228f5](https://github.com/rknightion/codexlb2otel/commit/e1228f500461e75f5ced55500bd18125faffb2e3))
* **live:** name the agents, and stop flattening the tree ([1964fff](https://github.com/rknightion/codexlb2otel/commit/1964fffffa268f739c7dcfba22610c184f526d63)), closes [#36](https://github.com/rknightion/codexlb2otel/issues/36)
* **live:** watch the conversation tree, running turns and all ([ff5d86d](https://github.com/rknightion/codexlb2otel/commit/ff5d86d27356c6e60d6b9c1280fbb33900680204)), closes [#35](https://github.com/rknightion/codexlb2otel/issues/35)
* mint release-please token from the OpenBao broker ([1499854](https://github.com/rknightion/codexlb2otel/commit/14998544f475c3028b7813a2837007841cbcc767))
* **tier:** carry the tier the client ASKED for, not just the one served ([fa0c997](https://github.com/rknightion/codexlb2otel/commit/fa0c99771f3d02b5752c8b4d853bb7b8592ccb0c)), closes [#33](https://github.com/rknightion/codexlb2otel/issues/33) [#34](https://github.com/rknightion/codexlb2otel/issues/34)


### Bug Fixes

* **agento11y:** name the agent, and speak the vocabulary sigil actually reads ([ab78f29](https://github.com/rknightion/codexlb2otel/commit/ab78f293d7ce70a0a7d8dd25bf99ad34d4577c79)), closes [#32](https://github.com/rknightion/codexlb2otel/issues/32)
* **ci:** the go-version job could not check out a private repo ([d331267](https://github.com/rknightion/codexlb2otel/commit/d331267266b3798295c3a065455f17c50b332d58))
* **ci:** the image publish job could not read its own repository ([edb39fd](https://github.com/rknightion/codexlb2otel/commit/edb39fdb155fcbaa6613f11f03f1246d9d4fa86f))
* **ci:** the published image carried no version tag ([0a103d4](https://github.com/rknightion/codexlb2otel/commit/0a103d44e21af829569dba9d1ad8a58e45c9667f))
* **clbsum:** find the config in its own container image ([ba62773](https://github.com/rknightion/codexlb2otel/commit/ba6277391be572230c6e08c814d791c54dd31d10)), closes [#40](https://github.com/rknightion/codexlb2otel/issues/40)
* **clbsum:** show what a run will cost, and say when it was interrupted ([6193bbf](https://github.com/rknightion/codexlb2otel/commit/6193bbf45170443088fde5d801ce401dfa0e71e8)), closes [#42](https://github.com/rknightion/codexlb2otel/issues/42)
* **tail:** publish progress per chunk, not per pass ([cdcf80d](https://github.com/rknightion/codexlb2otel/commit/cdcf80d6a97f73c2d9e2470d70cd15a023e5dfa9))
* **tail:** stop self-observability deadlocking the poll it observes ([ec6ecbc](https://github.com/rknightion/codexlb2otel/commit/ec6ecbc6fa57816f90b3bf6938dfeca8bcb131db))


### Performance

* **tail:** checkpoint on an interval, not on every poll ([22c8303](https://github.com/rknightion/codexlb2otel/commit/22c8303ec068e75e93f20edb54988734fefa57a7))


### Refactoring

* **compose:** give this exporter its own project, not codex-lb's ([eeceb5a](https://github.com/rknightion/codexlb2otel/commit/eeceb5a71edd25a067930f00cfbaf5a1ace45685))

## 0.1.0 (2026-08-07)


### Features

* **agento11y:** emit Generations to sigil, and read the per-item verdict ([d4cfe31](https://github.com/rknightion/codexlb2otel/commit/d4cfe31f0a37b5d86a64e9542dcf83f6f149a1b2)), closes [#19](https://github.com/rknightion/codexlb2otel/issues/19)
* **archive:** multi-member gzip tailer with byte-exact resume ([833948d](https://github.com/rknightion/codexlb2otel/commit/833948ddd42731a68887087ee1275010a9611a6c)), closes [#1](https://github.com/rknightion/codexlb2otel/issues/1)
* **attr,metrics:** prove the contract against the whole corpus, and make a dropped connection visible ([7f57ff8](https://github.com/rknightion/codexlb2otel/commit/7f57ff8405aaef0f19cc116edfcb8108aa39a08e)), closes [#3](https://github.com/rknightion/codexlb2otel/issues/3) [#17](https://github.com/rknightion/codexlb2otel/issues/17)
* **attr:** freeze the Turn -&gt; telemetry contract before the emitters fan out ([99e6731](https://github.com/rknightion/codexlb2otel/commit/99e6731fddb6063d214ec2d3c1207639d4907348))
* **attr:** line up with the GenAI conventions, and check the spec rather than recall it ([954bd03](https://github.com/rknightion/codexlb2otel/commit/954bd03cae030e5f005c471a5a667a72d9376e40)), closes [#18](https://github.com/rknightion/codexlb2otel/issues/18)
* capture what the full-corpus profile showed we were dropping ([6719bb3](https://github.com/rknightion/codexlb2otel/commit/6719bb35bd844b772849253e3de3ccaefdcdf189)), closes [#1](https://github.com/rknightion/codexlb2otel/issues/1)
* **clbfind:** scope a lookup to named archives, and shard both passes ([8f90bca](https://github.com/rknightion/codexlb2otel/commit/8f90bcaf76330a0c6ea16c8da52b161827803a62))
* **clbsync:** show sizes and totals so disk use is visible before fetching ([f285394](https://github.com/rknightion/codexlb2otel/commit/f28539426c3f4021dacd877bad435d6c3e665ab7))
* **corpus:** sync from camden and look up a response by id ([014b04b](https://github.com/rknightion/codexlb2otel/commit/014b04b9c5b18fc877cbe1c5d6cd2b66773805ab))
* **loki:** native push, one line per record type, rejections counted by reason ([c035046](https://github.com/rknightion/codexlb2otel/commit/c035046c8faf2171a74681bfde1a90e5755de468))
* **otlpmetric:** cumulative OTLP metrics from per-response deltas ([eb0b7b0](https://github.com/rknightion/codexlb2otel/commit/eb0b7b0fb3355e1bc8a300e9d13137a6bc5000ca))
* **otlptrace:** deterministic spans for a historical replay ([1479195](https://github.com/rknightion/codexlb2otel/commit/14791955353c66f9449999f43248eebc83ab580a))
* **packaging:** ship as a distroless container, with the healthcheck inside the binary ([cfc27d5](https://github.com/rknightion/codexlb2otel/commit/cfc27d554c907f0fdf7a2f1568559d3dd0cc2c98)), closes [#10](https://github.com/rknightion/codexlb2otel/issues/10)
* **probe:** detect archive format drift from the compressed files ([e3cb67d](https://github.com/rknightion/codexlb2otel/commit/e3cb67dd8d7697057f698df8194da80bdf37e58a)), closes [#1](https://github.com/rknightion/codexlb2otel/issues/1)
* **profile:** descend into embedded JSON documents, and stop a leak the descent exposed ([4d94522](https://github.com/rknightion/codexlb2otel/commit/4d945223031bc3d1fcaf0fde608f2782e1a57955)), closes [#21](https://github.com/rknightion/codexlb2otel/issues/21)
* **profile:** induce archive schemas instead of assuming them ([aa9b8b5](https://github.com/rknightion/codexlb2otel/commit/aa9b8b568132ea931540320ce01917002451d4b0)), closes [#1](https://github.com/rknightion/codexlb2otel/issues/1)
* **selfobs:** emit ingest lag and pipeline health, with dashboards and alerts to read them ([705ed00](https://github.com/rknightion/codexlb2otel/commit/705ed002d1c2ccebfe2a5ec4c4f3d35d71e9decb)), closes [#8](https://github.com/rknightion/codexlb2otel/issues/8) [#14](https://github.com/rknightion/codexlb2otel/issues/14)
* **service:** cmd/codexlb2otel, config loading and lifecycle ([78ab065](https://github.com/rknightion/codexlb2otel/commit/78ab0651ca273025e0da81fcd3c11e8d2de8a7f6))
* **service:** wire the three sinks, and close the guard bypass they found ([14391e7](https://github.com/rknightion/codexlb2otel/commit/14391e77d538d5108ae097fcf4b919e37b3c2d4e)), closes [#1](https://github.com/rknightion/codexlb2otel/issues/1)
* **sink:** emit the timing domains, and make the response chain queryable ([4ac0e92](https://github.com/rknightion/codexlb2otel/commit/4ac0e920126d52ea0a69759de40fbf8ad7c98a5c)), closes [#23](https://github.com/rknightion/codexlb2otel/issues/23) [#15](https://github.com/rknightion/codexlb2otel/issues/15)
* **sink:** split an oversized batch instead of dropping it, and bound tool-call arguments ([a13a574](https://github.com/rknightion/codexlb2otel/commit/a13a574181215d06f321336c6c40910cf7ce310c)), closes [#22](https://github.com/rknightion/codexlb2otel/issues/22)
* **tail:** follow the archive directory with byte-exact resume and opt-in reclaim ([a4ee197](https://github.com/rknightion/codexlb2otel/commit/a4ee1977c07642fc067b5677dfd87ad1487074b8))
* **turn:** adopt the timing domains that are actually there, plus the tool catalogue and request params ([4968921](https://github.com/rknightion/codexlb2otel/commit/49689213cfc5cfb0e1340ebfd6bb15d0613a9ed7)), closes [#12](https://github.com/rknightion/codexlb2otel/issues/12)
* **turn:** capture the input half of the conversation, harden against non-completion ([988390b](https://github.com/rknightion/codexlb2otel/commit/988390bc7618c34ac0481c9b8c5a01c9f1a28f40))
* **turn:** reduce frames to per-response turns with cumulative-metric deltas ([e7592ca](https://github.com/rknightion/codexlb2otel/commit/e7592ca0b8e8d1241d93528e9d410e6d2f5bd77e)), closes [#1](https://github.com/rknightion/codexlb2otel/issues/1)


### Bug Fixes

* **loki:** dotted labels 400, and over-age lines are accepted then discarded ([d025a9f](https://github.com/rknightion/codexlb2otel/commit/d025a9f34b04c75723e703f9c51eb0393d4f3e63))
* **otlptrace:** no span declared itself a GenAI inference ([228c717](https://github.com/rknightion/codexlb2otel/commit/228c7171ee134825cff76e24b594db35e47dce66)), closes [#11](https://github.com/rknightion/codexlb2otel/issues/11)
* **profile:** a FULL scan was only inducing the first 400 events per type ([0cada84](https://github.com/rknightion/codexlb2otel/commit/0cada84dcdf1fc8b7852ca307412823837042644))
* **profile:** stop reporting websocket control frames as a transport change ([a7464ee](https://github.com/rknightion/codexlb2otel/commit/a7464ee14fea13a51c6dc5ba38fc5410a3039182)), closes [#16](https://github.com/rknightion/codexlb2otel/issues/16)
* **test:** a corpus skip inside sync.Once left every later test asserting nothing ([d9992b5](https://github.com/rknightion/codexlb2otel/commit/d9992b5763e0a6cd5e3e8283855729a5fe6ece48))
* **turn:** a thread runs several counter series at once, so diff per series ([a052040](https://github.com/rknightion/codexlb2otel/commit/a052040a3b6d505f9d80a6114abd8a133315d3cc)), closes [#20](https://github.com/rknightion/codexlb2otel/issues/20)
