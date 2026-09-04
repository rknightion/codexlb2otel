# Changelog

## [0.4.0](https://github.com/rknightion/codexlb2otel/compare/v0.3.0...v0.4.0) (2026-09-04)


### Features

* add cost and bounded metric dimensions ([3897b83](https://github.com/rknightion/codexlb2otel/commit/3897b83e1fb4fa6fc2dbfde79d6e4c2f61ce0e42))
* add optional Postgres enrichment ([83063d0](https://github.com/rknightion/codexlb2otel/commit/83063d06da32f63b8c941900ed4d4a8eecd50f0e))
* adopt September wire attributes ([ea31d46](https://github.com/rknightion/codexlb2otel/commit/ea31d46b46df188821867a3c751a06967bf8de72))
* **ci:** add a ci-success aggregator and a renovate.json ([2f553c0](https://github.com/rknightion/codexlb2otel/commit/2f553c0211f55bfacd25e387abac05ecdcdf8dd6))
* **ci:** add govulncheck to align with the rest of the 2otel family ([4d3b268](https://github.com/rknightion/codexlb2otel/commit/4d3b2681bb218d56fb10ac0b98351675eed87cf0))
* evict stale reducer state ([d83368b](https://github.com/rknightion/codexlb2otel/commit/d83368b0d33e0a46835e96ad617615856f683ab2))
* expand generated observability dashboard ([7a54c59](https://github.com/rknightion/codexlb2otel/commit/7a54c593302466807693f78080eddfb127b09b47))
* expose requested tier across session signals ([6281483](https://github.com/rknightion/codexlb2otel/commit/62814838495b8109ebf91bcf49983e968d81fae6))
* freeze wave 1 telemetry seams ([84a0783](https://github.com/rknightion/codexlb2otel/commit/84a07834dd49c5ec0891755abb946a8b7ab1b671))
* restyle live monitor console ([ffa7ea0](https://github.com/rknightion/codexlb2otel/commit/ffa7ea09187b9a8d046ce535d4888db6fdd9ed0f))
* run archive drift probe in process ([472ff94](https://github.com/rknightion/codexlb2otel/commit/472ff94c1510f7d28a53db7ea8d28d71c4e65655))
* wire wave 1 runtime integration ([6a54f61](https://github.com/rknightion/codexlb2otel/commit/6a54f61bb369dfb558b8428e48b866448963a5a9))


### Bug Fixes

* address integrated review findings ([05c53ed](https://github.com/rknightion/codexlb2otel/commit/05c53edaa43feed0c497e64984e6ebd094bf4e4d))
* **agento11y:** align archive replay with Codex plugin ([ceb2d05](https://github.com/rknightion/codexlb2otel/commit/ceb2d05399f9c902a8cdff8e72fe567bf96a15da))
* **agento11y:** align generation trace and metric contracts ([0d7ac95](https://github.com/rknightion/codexlb2otel/commit/0d7ac95cf107704ae26624446b8a711ad8464e62))
* bound projected metric series ([334a4db](https://github.com/rknightion/codexlb2otel/commit/334a4db0f453c2da94d94e9f108aa429e9bc2a38))
* **ci:** repair two breaks the just migration introduced ([c0b64d3](https://github.com/rknightion/codexlb2otel/commit/c0b64d3efb6131347c791e05616287bddb059db5))
* **ci:** stop the archive probe persisting credentials ([bf291bb](https://github.com/rknightion/codexlb2otel/commit/bf291bbfccc89529d179f1299ae7d9e74c7c5474))
* decode direct HTTP payload objects ([a347d39](https://github.com/rknightion/codexlb2otel/commit/a347d398840c20287c04703ee821472f70894d64))
* decode duration prompt cache TTLs ([4cd37d3](https://github.com/rknightion/codexlb2otel/commit/4cd37d30f654adde3475a069ec0bb6e6cb5d07ce))
* decode HTTP archive envelopes ([3cb09fe](https://github.com/rknightion/codexlb2otel/commit/3cb09fe7d9bfe970720055335a2f563f9169ab01))
* **deps:** update module github.com/cenkalti/backoff/v5 to v7 ([#53](https://github.com/rknightion/codexlb2otel/issues/53)) ([98f2e51](https://github.com/rknightion/codexlb2otel/commit/98f2e51eb8e3b735ee26d47aada6c4730b9ed2de))
* **deps:** update module github.com/grafana/agento11y/go to v0.18.0 ([#49](https://github.com/rknightion/codexlb2otel/issues/49)) ([42b6881](https://github.com/rknightion/codexlb2otel/commit/42b6881cb1f3557f267657df5025412912f87471))
* **deps:** update module github.com/openrouterteam/go-sdk to v0.7.102 ([#69](https://github.com/rknightion/codexlb2otel/issues/69)) ([dd075b6](https://github.com/rknightion/codexlb2otel/commit/dd075b6248b536cef3837c922f6c32ab16089319))
* **deps:** update module github.com/openrouterteam/go-sdk to v0.7.103 ([#70](https://github.com/rknightion/codexlb2otel/issues/70)) ([6ce8e89](https://github.com/rknightion/codexlb2otel/commit/6ce8e890b9ffd835684ba3797be40c50f9fba094))
* **deps:** update module github.com/openrouterteam/go-sdk to v0.7.104 ([#71](https://github.com/rknightion/codexlb2otel/issues/71)) ([121c3c3](https://github.com/rknightion/codexlb2otel/commit/121c3c385b2543bccf76faa9cb774d3899e49ab5))
* **deps:** update module github.com/openrouterteam/go-sdk to v0.7.105 ([#72](https://github.com/rknightion/codexlb2otel/issues/72)) ([20bba52](https://github.com/rknightion/codexlb2otel/commit/20bba52297dbc01b3437922bbfe994087c28fd5c))
* **deps:** update module github.com/openrouterteam/go-sdk to v0.7.96 ([#46](https://github.com/rknightion/codexlb2otel/issues/46)) ([7ec692d](https://github.com/rknightion/codexlb2otel/commit/7ec692dfd1007c5be69e0e2c2926d44e36ba21bd))
* **deps:** update module github.com/openrouterteam/go-sdk to v0.7.97 ([#67](https://github.com/rknightion/codexlb2otel/issues/67)) ([3d63efc](https://github.com/rknightion/codexlb2otel/commit/3d63efc48d9a17073366b7cd5c4deaeb1928b4e4))
* **deps:** update module github.com/openrouterteam/go-sdk to v0.7.98 ([#68](https://github.com/rknightion/codexlb2otel/issues/68)) ([02d27e1](https://github.com/rknightion/codexlb2otel/commit/02d27e131250581701cc9bedd5f1a7223e9ef247))
* **deps:** update opentelemetry-go monorepo to v1.46.0 ([#50](https://github.com/rknightion/codexlb2otel/issues/50)) ([ac26b50](https://github.com/rknightion/codexlb2otel/commit/ac26b506b05f2253122bddda95fbe45a09652883))
* keep dashboard check runner portable ([13a12a5](https://github.com/rknightion/codexlb2otel/commit/13a12a51e0492dea5e4877205aca8b5b97b667a7))
* match Grafana dashboard canonical form ([f1f6a92](https://github.com/rknightion/codexlb2otel/commit/f1f6a921f70183eb637e8485ea6a0f57ce896fd6))
* refresh archive drift baseline ([1ad2396](https://github.com/rknightion/codexlb2otel/commit/1ad2396aee9f281e9611dbb95bc036aadfe68201))
* remove final ripgrep gate dependency ([eeb3e2b](https://github.com/rknightion/codexlb2otel/commit/eeb3e2b7a3b458df9fde59babbcc5022e1f447c6))
* separate routine corpus validation ([9381c3b](https://github.com/rknightion/codexlb2otel/commit/9381c3b153d11b3d47786e1ccd7fe2ed39008b1b))

## [0.3.0](https://github.com/m7kni/codexlb2otel/compare/v0.2.0...v0.3.0) (2026-08-23)


### Features

* **dashboards:** Model Usage tab, and fix silently-broken Prometheus tables ([c1a48cd](https://github.com/m7kni/codexlb2otel/commit/c1a48cd40c2fe9e1dd7a5f461664e341d84d9061))
* **dashboard:** surface fast mode effectiveness ([047f915](https://github.com/m7kni/codexlb2otel/commit/047f915b10d712a393db5f1d0d3974580c04d3ad))


### Bug Fixes

* **agento11y:** avoid subagent marker in agent names ([7de13e6](https://github.com/m7kni/codexlb2otel/commit/7de13e6c69fb9ab3dff4ee8b41875596d6feacab))
* **ci:** verify the CLI download and install it before minting the credential ([5ed6787](https://github.com/m7kni/codexlb2otel/commit/5ed67878ed488582ff8cf780c2eebaeb43635a32))

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
