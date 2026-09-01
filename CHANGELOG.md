# Changelog

## [0.10.3](https://github.com/dantech2000/refresh/compare/v0.10.2...v0.10.3) (2026-09-01)


### Bug Fixes

* **security:** harden pagination, HTTP reads, and concurrency bounds ([#254](https://github.com/dantech2000/refresh/issues/254)) ([9dec64e](https://github.com/dantech2000/refresh/commit/9dec64ec50bc3a05d55f8e00d879279921fc1344))
* **update:** decouple --force from health-gate skip; refuse 'latest' addon when k8s unknown ([#256](https://github.com/dantech2000/refresh/issues/256)) ([f0f1b55](https://github.com/dantech2000/refresh/commit/f0f1b5593210864a65e39fa2685f082b6fb240fc))

## [0.10.2](https://github.com/dantech2000/refresh/compare/v0.10.1...v0.10.2) (2026-08-31)


### Bug Fixes

* **deps:** bump github.com/aws/aws-sdk-go-v2/credentials ([#239](https://github.com/dantech2000/refresh/issues/239)) ([5bd71e5](https://github.com/dantech2000/refresh/commit/5bd71e59827bf642781d9fd92bbc74bb6ec44042))
* **deps:** bump github.com/aws/aws-sdk-go-v2/feature/ec2/imds ([#250](https://github.com/dantech2000/refresh/issues/250)) ([7b4f38f](https://github.com/dantech2000/refresh/commit/7b4f38f49d561b5fcf4061eaecf814eca7ebec2c))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/autoscaling ([#252](https://github.com/dantech2000/refresh/issues/252)) ([194f297](https://github.com/dantech2000/refresh/commit/194f2979e5620e540f5b9d0eb4b6b8a59abb1969))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/cloudwatch ([#241](https://github.com/dantech2000/refresh/issues/241)) ([8ac3712](https://github.com/dantech2000/refresh/commit/8ac371293f2171019382d20002b7313e87efc62e))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ec2 ([#237](https://github.com/dantech2000/refresh/issues/237)) ([909e64b](https://github.com/dantech2000/refresh/commit/909e64bc9f075532f578ba6ed6808c589f6b0b9a))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/eks ([#223](https://github.com/dantech2000/refresh/issues/223)) ([02a5ac4](https://github.com/dantech2000/refresh/commit/02a5ac4c8b12aef6283c76673dcf58b6e2bc398a))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/iam ([#233](https://github.com/dantech2000/refresh/issues/233)) ([6f0b3a9](https://github.com/dantech2000/refresh/commit/6f0b3a91aa091276cbeb7beae6eabf2fadb0ffc9))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding ([#236](https://github.com/dantech2000/refresh/issues/236)) ([02e01de](https://github.com/dantech2000/refresh/commit/02e01de3e95bfb2ec3be90ac874bf50c03291a87))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/servicequotas ([#231](https://github.com/dantech2000/refresh/issues/231)) ([ad6c535](https://github.com/dantech2000/refresh/commit/ad6c535686bef49c9d0c1202ba46de5034a9fb84))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/signin ([#247](https://github.com/dantech2000/refresh/issues/247)) ([859f55f](https://github.com/dantech2000/refresh/commit/859f55ff10020b2aec6e6357035a38f49a367ba8))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssm ([#221](https://github.com/dantech2000/refresh/issues/221)) ([1d2f3f1](https://github.com/dantech2000/refresh/commit/1d2f3f19513bd02318baddd9633e895646af9978))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/sso ([#232](https://github.com/dantech2000/refresh/issues/232)) ([19cb677](https://github.com/dantech2000/refresh/commit/19cb6777d79354e6ade790a6e3cf49cab8f749ff))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssooidc ([#238](https://github.com/dantech2000/refresh/issues/238)) ([37352ba](https://github.com/dantech2000/refresh/commit/37352ba983231c575db1e7064a002833eb3c9f22))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/sts ([#243](https://github.com/dantech2000/refresh/issues/243)) ([59303c1](https://github.com/dantech2000/refresh/commit/59303c17d471fc1b78a1ff938d300044997985a3))
* **deps:** bump github.com/go-openapi/jsonreference from 1.0.0 to 1.0.1 ([#225](https://github.com/dantech2000/refresh/issues/225)) ([9b3e569](https://github.com/dantech2000/refresh/commit/9b3e569d157b767a873149859310b922b5cb8d04))
* **deps:** bump github.com/go-openapi/swag from 0.28.0 to 0.29.1 ([#224](https://github.com/dantech2000/refresh/issues/224)) ([5c1cc23](https://github.com/dantech2000/refresh/commit/5c1cc23c3f7975821fe1929bea41a233194116dd))
* **deps:** bump github.com/go-openapi/swag/pools from 0.29.0 to 0.29.1 ([#222](https://github.com/dantech2000/refresh/issues/222)) ([bd5c70a](https://github.com/dantech2000/refresh/commit/bd5c70a665d6c6d755a5d4ca0f278be7974721dc))
* **deps:** bump github.com/mattn/go-runewidth from 0.0.27 to 0.0.28 ([#248](https://github.com/dantech2000/refresh/issues/248)) ([c25646a](https://github.com/dantech2000/refresh/commit/c25646a2d483a4f8ecbbfbd0e9269a9f19e8b91f))
* **deps:** bump github.com/urfave/cli/v3 from 3.10.1 to 3.11.0 ([#245](https://github.com/dantech2000/refresh/issues/245)) ([c897bee](https://github.com/dantech2000/refresh/commit/c897bee00ccfd0f361a6431661f22ee59999bd87))
* **deps:** bump k8s.io/metrics from 0.36.4 to 0.37.0 ([#234](https://github.com/dantech2000/refresh/issues/234)) ([3549894](https://github.com/dantech2000/refresh/commit/3549894f03db93067d8683f308c20470d7ec5148))

## [0.10.1](https://github.com/dantech2000/refresh/compare/v0.10.0...v0.10.1) (2026-08-26)


### Bug Fixes

* **deps:** bump github.com/aws/aws-sdk-go-v2 from 1.43.4 to 1.43.5 ([#167](https://github.com/dantech2000/refresh/issues/167)) ([18552a1](https://github.com/dantech2000/refresh/commit/18552a1eab1b2501b287a4a27bc7cab6f2ee2425))
* **deps:** bump github.com/aws/aws-sdk-go-v2/config ([#191](https://github.com/dantech2000/refresh/issues/191)) ([004e996](https://github.com/dantech2000/refresh/commit/004e996fa86c6386cccacc7a188470fb67b4a448))
* **deps:** bump github.com/aws/aws-sdk-go-v2/config ([#207](https://github.com/dantech2000/refresh/issues/207)) ([8e048b3](https://github.com/dantech2000/refresh/commit/8e048b37149d9de5527aa557b15e59b5250a5ca1))
* **deps:** bump github.com/aws/aws-sdk-go-v2/feature/ec2/imds ([#189](https://github.com/dantech2000/refresh/issues/189)) ([0472b31](https://github.com/dantech2000/refresh/commit/0472b31ce4c1ef87db439370c5acbc8e0aa74e91))
* **deps:** bump github.com/aws/aws-sdk-go-v2/feature/ec2/imds ([#200](https://github.com/dantech2000/refresh/issues/200)) ([59b0053](https://github.com/dantech2000/refresh/commit/59b0053b51f754307d37998a4188fac609882f78))
* **deps:** bump github.com/aws/aws-sdk-go-v2/internal/v4a ([#170](https://github.com/dantech2000/refresh/issues/170)) ([cff00f3](https://github.com/dantech2000/refresh/commit/cff00f3d5176018fb0bd71bc6dbb5f000326986f))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/autoscaling ([#184](https://github.com/dantech2000/refresh/issues/184)) ([1a105e0](https://github.com/dantech2000/refresh/commit/1a105e0b8b74189c789087a47c24889e4ab72c65))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/autoscaling ([#193](https://github.com/dantech2000/refresh/issues/193)) ([7df3d4d](https://github.com/dantech2000/refresh/commit/7df3d4dd5a58da9a56f9d9884262d73785ff986b))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/cloudwatch ([#182](https://github.com/dantech2000/refresh/issues/182)) ([7a55e1c](https://github.com/dantech2000/refresh/commit/7a55e1cd15a655e48e1bdacf4dc5069336666fae))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/cloudwatch ([#219](https://github.com/dantech2000/refresh/issues/219)) ([c2624d7](https://github.com/dantech2000/refresh/commit/c2624d7afd34716e02f5d2c374833e5c63e4b034))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ec2 ([#175](https://github.com/dantech2000/refresh/issues/175)) ([6a21014](https://github.com/dantech2000/refresh/commit/6a21014d864bbe23aa92df9e3b4e1db384b6c597))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ec2 ([#205](https://github.com/dantech2000/refresh/issues/205)) ([e147d89](https://github.com/dantech2000/refresh/commit/e147d89a556a2bdc126fa4792414f93b9e242161))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/eks ([#188](https://github.com/dantech2000/refresh/issues/188)) ([69bcaee](https://github.com/dantech2000/refresh/commit/69bcaeeea5a039feb28dc8ee4c46ea9254ec6113))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/eks ([#195](https://github.com/dantech2000/refresh/issues/195)) ([e07b697](https://github.com/dantech2000/refresh/commit/e07b69791c2ac78baa1e0d82e731414b0d94ded5))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/iam ([#180](https://github.com/dantech2000/refresh/issues/180)) ([19bf9a8](https://github.com/dantech2000/refresh/commit/19bf9a85c4cdd4790fad452ab63b5a4dd50be49b))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/iam ([#218](https://github.com/dantech2000/refresh/issues/218)) ([8e44825](https://github.com/dantech2000/refresh/commit/8e4482535ae598ef8034315134e4f485fc16cd12))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/internal/presigned-url ([#166](https://github.com/dantech2000/refresh/issues/166)) ([9277dc7](https://github.com/dantech2000/refresh/commit/9277dc7e77504c61456193db1fb2953f20d04e7d))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/servicequotas ([#172](https://github.com/dantech2000/refresh/issues/172)) ([73cc84a](https://github.com/dantech2000/refresh/commit/73cc84ab22d8e1555c9ec3fd02300a1afb4416df))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/servicequotas ([#211](https://github.com/dantech2000/refresh/issues/211)) ([96e5b51](https://github.com/dantech2000/refresh/commit/96e5b514862600f2cfb9fc4ae3c0db877433c064))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/signin ([#174](https://github.com/dantech2000/refresh/issues/174)) ([71abb9b](https://github.com/dantech2000/refresh/commit/71abb9bb3a6725f08b827a6a274cc81a976926a0))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/signin ([#208](https://github.com/dantech2000/refresh/issues/208)) ([5f4565b](https://github.com/dantech2000/refresh/commit/5f4565b754ff44eeee70987cc030593d7e3e7b64))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssm ([#183](https://github.com/dantech2000/refresh/issues/183)) ([341b69e](https://github.com/dantech2000/refresh/commit/341b69ef320933a378fdd76714372daf3abaab47))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssm ([#213](https://github.com/dantech2000/refresh/issues/213)) ([201061a](https://github.com/dantech2000/refresh/commit/201061a3bb4065b00548693a0b728b3b1b77fa0d))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/sso ([#173](https://github.com/dantech2000/refresh/issues/173)) ([187dfd2](https://github.com/dantech2000/refresh/commit/187dfd2849439aebef4fd1f02066b34376a24a8b))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/sso ([#203](https://github.com/dantech2000/refresh/issues/203)) ([75d429d](https://github.com/dantech2000/refresh/commit/75d429df75e8e03917c02272a63f9a9bb12d2c1d))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssooidc ([#186](https://github.com/dantech2000/refresh/issues/186)) ([324c239](https://github.com/dantech2000/refresh/commit/324c23963c2c632ba207c87b01d278d709a50197))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssooidc ([#202](https://github.com/dantech2000/refresh/issues/202)) ([c624219](https://github.com/dantech2000/refresh/commit/c62421992c992679a3be094bc4f4ce05885f6a98))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/sts ([#168](https://github.com/dantech2000/refresh/issues/168)) ([0dac7cc](https://github.com/dantech2000/refresh/commit/0dac7cc39a3055c36fa7258b312098aa2629cf4a))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/sts ([#192](https://github.com/dantech2000/refresh/issues/192)) ([656786c](https://github.com/dantech2000/refresh/commit/656786c9b98c916d82767b5e2e184a51d4968e30))
* **deps:** bump github.com/aws/smithy-go from 1.27.6 to 1.27.7 ([#176](https://github.com/dantech2000/refresh/issues/176)) ([6c8ac12](https://github.com/dantech2000/refresh/commit/6c8ac1202d5adc9dde430a7b007bcb926c2166ef))
* **deps:** bump github.com/fxamacker/cbor/v2 from 2.9.2 to 2.9.3 ([#199](https://github.com/dantech2000/refresh/issues/199)) ([e354b64](https://github.com/dantech2000/refresh/commit/e354b641191b39a93980fea819846b230c1e4eca))
* **deps:** bump github.com/go-openapi/swag/cmdutils ([#210](https://github.com/dantech2000/refresh/issues/210)) ([674d0e4](https://github.com/dantech2000/refresh/commit/674d0e4f7b4146e2616ab22f4629a981ac0ffed8))
* **deps:** bump github.com/go-openapi/swag/fileutils ([#216](https://github.com/dantech2000/refresh/issues/216)) ([1342cca](https://github.com/dantech2000/refresh/commit/1342ccad84a1a804a8db5e8cbdf5c6ea05743712))
* **deps:** bump github.com/go-openapi/swag/loading from 0.28.0 to 0.29.0 ([#197](https://github.com/dantech2000/refresh/issues/197)) ([bb11327](https://github.com/dantech2000/refresh/commit/bb11327eacebd4b890ea465f89ec76d1d806bf4e))
* **deps:** bump github.com/go-openapi/swag/mangling ([#214](https://github.com/dantech2000/refresh/issues/214)) ([3737ca7](https://github.com/dantech2000/refresh/commit/3737ca744bdfdf7a88f3e42d9b18867fdecf1186))
* **deps:** bump github.com/go-openapi/swag/netutils ([#196](https://github.com/dantech2000/refresh/issues/196)) ([b30df79](https://github.com/dantech2000/refresh/commit/b30df7945c4fc30932ef479464f98c1a20e91312))
* **deps:** bump github.com/go-openapi/swag/typeutils ([#201](https://github.com/dantech2000/refresh/issues/201)) ([47c5304](https://github.com/dantech2000/refresh/commit/47c5304011df3bcf883ecafb90e5acae6b10bc15))
* **deps:** bump github.com/xo/terminfo ([#181](https://github.com/dantech2000/refresh/issues/181)) ([90422fc](https://github.com/dantech2000/refresh/commit/90422fc9c608382a9f6109ae8f2902663d8750cb))
* **deps:** bump golang.org/x/net from 0.57.0 to 0.58.0 ([#185](https://github.com/dantech2000/refresh/issues/185)) ([4972e3b](https://github.com/dantech2000/refresh/commit/4972e3b4542cc0f6c9fa9adba308b9341cfdfa92))
* **deps:** bump golang.org/x/text from 0.40.0 to 0.41.0 ([#171](https://github.com/dantech2000/refresh/issues/171)) ([bf8ca04](https://github.com/dantech2000/refresh/commit/bf8ca04bf74eda11867cef29ea0a93a9b0fef9ad))
* **deps:** bump google.golang.org/protobuf ([#179](https://github.com/dantech2000/refresh/issues/179)) ([161e5d3](https://github.com/dantech2000/refresh/commit/161e5d3dd5b52f77b162f1d3ebd5568bf792beb9))
* **deps:** bump k8s.io/client-go from 0.36.3 to 0.36.4 ([#198](https://github.com/dantech2000/refresh/issues/198)) ([a608004](https://github.com/dantech2000/refresh/commit/a60800442e2e8f89ea9505dcbde06599a4ea50f4))
* **deps:** bump k8s.io/metrics from 0.36.3 to 0.36.4 ([#209](https://github.com/dantech2000/refresh/issues/209)) ([e5af74a](https://github.com/dantech2000/refresh/commit/e5af74a48711de0268a27fdf2c4b68b64db50f6f))

## [0.10.0](https://github.com/dantech2000/refresh/compare/v0.9.4...v0.10.0) (2026-08-12)


### Features

* **noderoll:** watch-backed live roll view via informers ([#163](https://github.com/dantech2000/refresh/issues/163)) ([a87cb49](https://github.com/dantech2000/refresh/commit/a87cb491d02810c6f01b4313a2558ac8d541be79))


### Bug Fixes

* **deps:** bump github.com/aws/aws-sdk-go-v2 from 1.43.2 to 1.43.4 ([#154](https://github.com/dantech2000/refresh/issues/154)) ([f41ad21](https://github.com/dantech2000/refresh/commit/f41ad210798d8977a533c38378fbcbaa0e257bed))
* **deps:** bump github.com/aws/aws-sdk-go-v2/config ([#145](https://github.com/dantech2000/refresh/issues/145)) ([e88f872](https://github.com/dantech2000/refresh/commit/e88f872bb443888ed904db71e2770245bb828dfa))
* **deps:** bump github.com/aws/aws-sdk-go-v2/feature/ec2/imds ([#150](https://github.com/dantech2000/refresh/issues/150)) ([e5894d7](https://github.com/dantech2000/refresh/commit/e5894d7af184c09f4b646535384918b1b9f654e8))
* **deps:** bump github.com/aws/aws-sdk-go-v2/internal/v4a ([#156](https://github.com/dantech2000/refresh/issues/156)) ([955d23b](https://github.com/dantech2000/refresh/commit/955d23b8a9e1d48ce24e790b69d751bf8cee97cb))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/autoscaling ([#158](https://github.com/dantech2000/refresh/issues/158)) ([1d2fdcf](https://github.com/dantech2000/refresh/commit/1d2fdcff14140b8394d1990e038f714be63dddee))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/cloudwatch ([#148](https://github.com/dantech2000/refresh/issues/148)) ([c1b531c](https://github.com/dantech2000/refresh/commit/c1b531ca20fbc34ddb0eafdf69477cafa9db4e21))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/eks ([#144](https://github.com/dantech2000/refresh/issues/144)) ([cb75e83](https://github.com/dantech2000/refresh/commit/cb75e83d1133618b58f46aa3816f05a33e226e17))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/iam ([#151](https://github.com/dantech2000/refresh/issues/151)) ([c265aa6](https://github.com/dantech2000/refresh/commit/c265aa6b9ba8a9791dfa08d3991e5a280753eb48))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding ([#155](https://github.com/dantech2000/refresh/issues/155)) ([7f7af70](https://github.com/dantech2000/refresh/commit/7f7af7011b31d909ea871df07ecba0b13defb91f))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/internal/presigned-url ([#160](https://github.com/dantech2000/refresh/issues/160)) ([7c84510](https://github.com/dantech2000/refresh/commit/7c84510ac5cc1ac66d807f88f4ebdb9ceb57989b))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/servicequotas ([#162](https://github.com/dantech2000/refresh/issues/162)) ([5aa2673](https://github.com/dantech2000/refresh/commit/5aa2673fa3018067a45cc1fec299402c4b641b47))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/signin ([#147](https://github.com/dantech2000/refresh/issues/147)) ([6148007](https://github.com/dantech2000/refresh/commit/614800734f5d6c2d8dd4e62b2d3cdf824e243ac0))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssm ([#161](https://github.com/dantech2000/refresh/issues/161)) ([5081971](https://github.com/dantech2000/refresh/commit/5081971dc24a348e3b53a8f7467300b4f7d6b167))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/sso ([#142](https://github.com/dantech2000/refresh/issues/142)) ([cccfb0a](https://github.com/dantech2000/refresh/commit/cccfb0aedb7616faae984c9b8c64a89e9fe5c494))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssooidc ([#159](https://github.com/dantech2000/refresh/issues/159)) ([74d57b0](https://github.com/dantech2000/refresh/commit/74d57b034029b9e8b96b2631af3abac40fdd430c))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/sts ([#157](https://github.com/dantech2000/refresh/issues/157)) ([bb1c06c](https://github.com/dantech2000/refresh/commit/bb1c06c17a69df6e26280f60ae823f4a3b00e64b))
* **deps:** bump github.com/aws/smithy-go from 1.27.5 to 1.27.6 ([#153](https://github.com/dantech2000/refresh/issues/153)) ([a42269e](https://github.com/dantech2000/refresh/commit/a42269e15acddfdc9b1a286f3bfd91b5db76f31a))

## [0.9.4](https://github.com/dantech2000/refresh/compare/v0.9.3...v0.9.4) (2026-08-04)


### Bug Fixes

* **deps:** bump github.com/aws/aws-sdk-go-v2/config ([#110](https://github.com/dantech2000/refresh/issues/110)) ([99c0d1b](https://github.com/dantech2000/refresh/commit/99c0d1bf4b8e1776ad33079644d845cafd9e0c87))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/autoscaling ([#109](https://github.com/dantech2000/refresh/issues/109)) ([6e09815](https://github.com/dantech2000/refresh/commit/6e09815e583f3d5791901be57693e36b330dc6f5))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/cloudwatch ([#136](https://github.com/dantech2000/refresh/issues/136)) ([2528d8b](https://github.com/dantech2000/refresh/commit/2528d8b56655b3885108788194cfc2c8f3a86cfc))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ec2 ([#128](https://github.com/dantech2000/refresh/issues/128)) ([1e2b7bb](https://github.com/dantech2000/refresh/commit/1e2b7bb7cd088841f4213f56552ed8709c34c842))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/eks ([#127](https://github.com/dantech2000/refresh/issues/127)) ([e0a10b6](https://github.com/dantech2000/refresh/commit/e0a10b665885cd9fec981c9b5e0128bac198052d))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/iam ([#130](https://github.com/dantech2000/refresh/issues/130)) ([db73a5d](https://github.com/dantech2000/refresh/commit/db73a5d202356dc31fa183d27d9f04f2fb2a961d))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/servicequotas ([#138](https://github.com/dantech2000/refresh/issues/138)) ([b5d2d50](https://github.com/dantech2000/refresh/commit/b5d2d504ef2cf53fbe1ecb07bd2c0ecf9b07db6d))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssm ([#135](https://github.com/dantech2000/refresh/issues/135)) ([d7efe75](https://github.com/dantech2000/refresh/commit/d7efe75886a0a2954d021684d1d91ba668e1554f))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssooidc ([#113](https://github.com/dantech2000/refresh/issues/113)) ([c6d6c19](https://github.com/dantech2000/refresh/commit/c6d6c19b0e05bfdd4039b436a92484b92db022fb))
* **deps:** bump github.com/cpuguy83/go-md2man/v2 from 2.0.5 to 2.0.7 ([#115](https://github.com/dantech2000/refresh/issues/115)) ([d6eda9d](https://github.com/dantech2000/refresh/commit/d6eda9d0d4a02c93545e8cfe2749324ca10904ca))
* **deps:** bump github.com/fxamacker/cbor/v2 from 2.9.0 to 2.9.2 ([#107](https://github.com/dantech2000/refresh/issues/107)) ([e1040f3](https://github.com/dantech2000/refresh/commit/e1040f38aa00bf233953218e725212db02727191))
* **deps:** bump github.com/go-logr/logr from 1.4.3 to 1.4.4 ([#108](https://github.com/dantech2000/refresh/issues/108)) ([d41f02e](https://github.com/dantech2000/refresh/commit/d41f02e923a798cccb4f9b47c5da79c3c5b97862))
* **deps:** bump github.com/go-openapi/jsonreference from 0.20.2 to 1.0.0 ([#118](https://github.com/dantech2000/refresh/issues/118)) ([89146fb](https://github.com/dantech2000/refresh/commit/89146fb5885ff35b53d1f150614f62ef55c21aa2))
* **deps:** bump github.com/google/gnostic-models from 0.7.0 to 0.7.1 ([#129](https://github.com/dantech2000/refresh/issues/129)) ([6dca213](https://github.com/dantech2000/refresh/commit/6dca21314078f288714210b42bee7fe1a3be34f8))
* **deps:** bump github.com/mattn/go-runewidth from 0.0.24 to 0.0.27 ([#132](https://github.com/dantech2000/refresh/issues/132)) ([c5678b9](https://github.com/dantech2000/refresh/commit/c5678b9678edd9d340111f362df8bf5580f90d84))
* **deps:** bump github.com/spf13/pflag from 1.0.9 to 1.0.10 ([#121](https://github.com/dantech2000/refresh/issues/121)) ([90d3e4f](https://github.com/dantech2000/refresh/commit/90d3e4f4d6cc11e56022dafb06dbd7b028b1ebb9))
* **deps:** bump go.yaml.in/yaml/v2 from 2.4.3 to 2.4.4 ([#134](https://github.com/dantech2000/refresh/issues/134)) ([55466fc](https://github.com/dantech2000/refresh/commit/55466fc2c49f4c5490bbe5c062a00f1c1f2fb23f))
* **deps:** bump go.yaml.in/yaml/v3 from 3.0.4 to 3.0.5 ([#131](https://github.com/dantech2000/refresh/issues/131)) ([a6706da](https://github.com/dantech2000/refresh/commit/a6706da92673a5bb8223d93c8684068318237983))
* **deps:** bump golang.org/x/net from 0.56.0 to 0.57.0 ([#116](https://github.com/dantech2000/refresh/issues/116)) ([b7a4eaa](https://github.com/dantech2000/refresh/commit/b7a4eaaebcb1f880b825b7e1393359d1d3b3783a))
* **deps:** bump golang.org/x/oauth2 from 0.34.0 to 0.36.0 ([#137](https://github.com/dantech2000/refresh/issues/137)) ([3895d73](https://github.com/dantech2000/refresh/commit/3895d737cc9096703aea7b49d93bf7d28589c9cb))
* **deps:** bump golang.org/x/term from 0.44.0 to 0.45.0 ([#117](https://github.com/dantech2000/refresh/issues/117)) ([0ff15ae](https://github.com/dantech2000/refresh/commit/0ff15aebbe9f4b0153d1d55afe04f3670ec79dd7))
* **deps:** bump golang.org/x/time from 0.14.0 to 0.15.0 ([#111](https://github.com/dantech2000/refresh/issues/111)) ([62ea4cc](https://github.com/dantech2000/refresh/commit/62ea4cc9576e0678fe424f5bf0bab3cfbef796fe))
* **deps:** bump sigs.k8s.io/structured-merge-diff/v6 from 6.3.3 to 6.4.2 ([#126](https://github.com/dantech2000/refresh/issues/126)) ([e965a8a](https://github.com/dantech2000/refresh/commit/e965a8aff6561b265804936875917ba7fc1b05a7))

## [0.9.3](https://github.com/dantech2000/refresh/compare/v0.9.2...v0.9.3) (2026-07-29)


### Bug Fixes

* **deps:** bump github.com/aws/aws-sdk-go-v2/credentials ([#103](https://github.com/dantech2000/refresh/issues/103)) ([2ab65ca](https://github.com/dantech2000/refresh/commit/2ab65ca04514c89513c97e0a9c63d9aa6fa758d6))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/autoscaling ([#96](https://github.com/dantech2000/refresh/issues/96)) ([bf5dcc9](https://github.com/dantech2000/refresh/commit/bf5dcc929217c3ccbef1ed453c0c265439785baa))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/iam ([#102](https://github.com/dantech2000/refresh/issues/102)) ([c5c287e](https://github.com/dantech2000/refresh/commit/c5c287e41ff22cdd7ff19664f7b12fa6ea0501bb))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/ssm ([#98](https://github.com/dantech2000/refresh/issues/98)) ([1997913](https://github.com/dantech2000/refresh/commit/199791391ce436ad989122794ab96ec89e83effb))
* **deps:** bump github.com/go-openapi/swag from 0.23.0 to 0.28.0 ([#83](https://github.com/dantech2000/refresh/issues/83)) ([174d24d](https://github.com/dantech2000/refresh/commit/174d24d4cbc5d801e4e2df27ddc2562320f65f9c))

## [0.9.2](https://github.com/dantech2000/refresh/compare/v0.9.1...v0.9.2) (2026-07-29)


### Bug Fixes

* **ci:** merge Dependabot PRs as a user so CI and releases trigger ([#99](https://github.com/dantech2000/refresh/issues/99)) ([ab21bd1](https://github.com/dantech2000/refresh/commit/ab21bd1351b781651efd024fbf18a4b19ed303d1))
* **ci:** merge Dependabot PRs directly instead of via --auto ([#104](https://github.com/dantech2000/refresh/issues/104)) ([20e704f](https://github.com/dantech2000/refresh/commit/20e704fb2672af66605482c9678e0d273b589e0f))
* **deps:** bump atomicgo.dev/keyboard from 0.2.9 to 0.2.10 ([#82](https://github.com/dantech2000/refresh/issues/82)) ([65b2c91](https://github.com/dantech2000/refresh/commit/65b2c91322a156775a185718b7ca92050d8d49b6))
* **deps:** bump github.com/aws/aws-sdk-go-v2/feature/ec2/imds ([#94](https://github.com/dantech2000/refresh/issues/94)) ([da5d330](https://github.com/dantech2000/refresh/commit/da5d330a62f96c47680450b9a0f906d55ac0cd3a))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/cloudwatch ([#95](https://github.com/dantech2000/refresh/issues/95)) ([b6e8b05](https://github.com/dantech2000/refresh/commit/b6e8b05f65de51e9891f64f328d86331f6a42dd8))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding ([#100](https://github.com/dantech2000/refresh/issues/100)) ([37f1d0f](https://github.com/dantech2000/refresh/commit/37f1d0fbd8883827dc0411643ac4c3a3d5111483))
* **deps:** bump github.com/aws/aws-sdk-go-v2/service/servicequotas ([#90](https://github.com/dantech2000/refresh/issues/90)) ([698e701](https://github.com/dantech2000/refresh/commit/698e701f75ca9b231131ceb1d0a6f04020151459))
* **deps:** bump github.com/gookit/color from 1.6.0 to 1.6.1 ([#91](https://github.com/dantech2000/refresh/issues/91)) ([7e40ff9](https://github.com/dantech2000/refresh/commit/7e40ff9c6d2527ddcf60be220458c36034e100c7))
* **deps:** bump github.com/mattn/go-colorable from 0.1.14 to 0.1.15 ([#86](https://github.com/dantech2000/refresh/issues/86)) ([42064a3](https://github.com/dantech2000/refresh/commit/42064a33f9c761103f5cf75c564e155006d4abd4))
* **deps:** bump github.com/mattn/go-isatty from 0.0.22 to 0.0.24 ([#81](https://github.com/dantech2000/refresh/issues/81)) ([fb059dc](https://github.com/dantech2000/refresh/commit/fb059dc32e83a719f533f2dc8b6c5fadd36a79de))
* **deps:** bump github.com/urfave/cli/v3 from 3.9.1 to 3.10.1 ([#80](https://github.com/dantech2000/refresh/issues/80)) ([7f989e8](https://github.com/dantech2000/refresh/commit/7f989e8e83f9d3158b82ec8ef7ea699bb6db2b71))
* **deps:** bump golang.org/x/sys from 0.46.0 to 0.47.0 ([#88](https://github.com/dantech2000/refresh/issues/88)) ([b368ce3](https://github.com/dantech2000/refresh/commit/b368ce3433bfce95f5f8a9814de4cd6db0db87c6))
* **deps:** bump k8s.io/metrics from 0.36.2 to 0.36.3 ([#89](https://github.com/dantech2000/refresh/issues/89)) ([baa5d17](https://github.com/dantech2000/refresh/commit/baa5d179fd1e10d68cf274ee0ef6dc1b5891e13d))

## [0.9.1](https://github.com/dantech2000/refresh/compare/v0.9.0...v0.9.1) (2026-07-29)


### Bug Fixes

* rebuild release binaries against patched x/text and Go 1.26.5 ([4185a11](https://github.com/dantech2000/refresh/commit/4185a1170db8c640a43194d161b7cfffcb4d520b))

## [0.9.0](https://github.com/dantech2000/refresh/compare/v0.8.0...v0.9.0) (2026-06-15)


### Features

* **upgrade-check:** themed insight detail view + discoverable insight IDs (REF-149) ([#69](https://github.com/dantech2000/refresh/issues/69)) ([e546903](https://github.com/dantech2000/refresh/commit/e546903a9b7764af16c26b278ab1790807c6e47e))


### Bug Fixes

* **health:** wire all checks consistently + don't let skipped checks force WARN (REF-148) ([#67](https://github.com/dantech2000/refresh/issues/67)) ([abc3aa0](https://github.com/dantech2000/refresh/commit/abc3aa0cbc48c5ae2f2ffd8af60510c0e919c426))

## [0.8.0](https://github.com/dantech2000/refresh/compare/v0.7.0...v0.8.0) (2026-06-15)


### Features

* **cluster:** itemize per-check health results in cluster describe (REF-146) ([#65](https://github.com/dantech2000/refresh/issues/65)) ([2c1443c](https://github.com/dantech2000/refresh/commit/2c1443cd1722e329c7c5904c81d091fc0739b347))
* **health:** control-plane readiness gate from AWS/EKS CloudWatch metrics (REF-140) ([#59](https://github.com/dantech2000/refresh/issues/59)) ([96ae965](https://github.com/dantech2000/refresh/commit/96ae965b23abc7f2459f4e64fc1bf4eff4381acf))
* **health:** EC2 vCPU service-quota headroom pre-flight (REF-144) ([#63](https://github.com/dantech2000/refresh/issues/63)) ([cf44489](https://github.com/dantech2000/refresh/commit/cf44489e5e3902f8e68c843f3684e89cfd331df9))
* **health:** live node CPU+memory drain headroom via metrics-server (REF-142) ([#60](https://github.com/dantech2000/refresh/issues/60)) ([bdd6af4](https://github.com/dantech2000/refresh/commit/bdd6af4f48a45cf34d43e1deb4073e1992beed8e))
* **nodegroup:** pre-flight instance-type availability per AZ on scale/update (REF-143) ([#62](https://github.com/dantech2000/refresh/issues/62)) ([164b293](https://github.com/dantech2000/refresh/commit/164b293eb54682e3dbbbe3fd0ef0ba05a830384c))
* **noderoll:** surface live Kubernetes Warning events during a roll (REF-138) ([#61](https://github.com/dantech2000/refresh/issues/61)) ([7f6428d](https://github.com/dantech2000/refresh/commit/7f6428df478ae33f466e98372d2421caf06b6995))


### Bug Fixes

* CLI/help/status UX bugs (REF-129, REF-131, REF-132, REF-133, REF-134) ([#51](https://github.com/dantech2000/refresh/issues/51)) ([e0d9010](https://github.com/dantech2000/refresh/commit/e0d90104e8f5509bafd8f64e61319859efb0398b))
* **nodegroup,cluster:** measure real node readiness instead of synthesizing it (REF-130) ([#52](https://github.com/dantech2000/refresh/issues/52)) ([17fc71f](https://github.com/dantech2000/refresh/commit/17fc71f68373b42a7448149f9658de4a67ea9423))

## [0.7.0](https://github.com/dantech2000/refresh/compare/v0.6.0...v0.7.0) (2026-06-13)


### Features

* **cluster:** upgrade-check — EKS Cluster Insights + version-skew readiness (REF-12) ([#31](https://github.com/dantech2000/refresh/issues/31)) ([16d6fdf](https://github.com/dantech2000/refresh/commit/16d6fdfd3e282a21623ec06cd2d8293b89fd6721))
* **docs:** generate the command/flag reference from the CLI tree (REF-108) ([#43](https://github.com/dantech2000/refresh/issues/43)) ([dac59b4](https://github.com/dantech2000/refresh/commit/dac59b459552b3a3f17f36de9ab7c590a8b5d8a8))
* **health:** --kubeconfig flag + connection diagnostics for pre-flight checks (REF-3) ([#32](https://github.com/dantech2000/refresh/issues/32)) ([0b8f1ac](https://github.com/dantech2000/refresh/commit/0b8f1ac8047509fc46035a868bf29da52d01b6bd))
* **nodegroup:** AMI refresh flagship — fleet mode, verification, changelog, unattended, custom-AMI safety (REF-80) ([#33](https://github.com/dantech2000/refresh/issues/33)) ([601274c](https://github.com/dantech2000/refresh/commit/601274ca98470b44cb9d9afbfb3f800272e8d1d3))
* output redesign + live cluster-roll observability (REF-119) ([#50](https://github.com/dantech2000/refresh/issues/50)) ([3dbc24e](https://github.com/dantech2000/refresh/commit/3dbc24e866153a150a2c6ebe7e657d439aae1bff))
* signal cancellation + mechanical hygiene (salvage of [#19](https://github.com/dantech2000/refresh/issues/19)/[#21](https://github.com/dantech2000/refresh/issues/21)) ([#25](https://github.com/dantech2000/refresh/issues/25)) ([5b27147](https://github.com/dantech2000/refresh/commit/5b2714720d8424c2d037524014be9910ab67c728))
* **status:** refresh status — fleet patch posture across clusters/regions (REF-79) ([#30](https://github.com/dantech2000/refresh/issues/30)) ([1e9cc61](https://github.com/dantech2000/refresh/commit/1e9cc612bb2d0782bdf8de95ac5cff300b128107))


### Bug Fixes

* **cli:** consistency & robustness hardening — flags, positional, ctx-cancel, nil-derefs (REF-52) ([#38](https://github.com/dantech2000/refresh/issues/38)) ([af93607](https://github.com/dantech2000/refresh/commit/af9360768496f4b290d2bec00e7ad085b32efdae))
* **cli:** output & flag correctness — filters, format validation, global region/profile, yaml keys ([#34](https://github.com/dantech2000/refresh/issues/34)) ([2a06a7f](https://github.com/dantech2000/refresh/commit/2a06a7f1b01b1b58b1a6fbcf468c9734e640c8b8))
* harden defensive nil-checks and input validation (REF-115, REF-116, REF-117) ([#47](https://github.com/dantech2000/refresh/issues/47)) ([771e487](https://github.com/dantech2000/refresh/commit/771e4877b51c1ac476a8b4ff9215f582db529d53))
* **health:** scoring accuracy — skip exclusion, peak CPU, proxy honesty, std-dev relabel (REF-63) ([#29](https://github.com/dantech2000/refresh/issues/29)) ([5445a23](https://github.com/dantech2000/refresh/commit/5445a23d6e0f167112b73881d1ce9b1b6f8f64aa))
* **ui:** output/formatting data-integrity — TSV escaping, zero-time, display-cell widths (REF-62) ([#37](https://github.com/dantech2000/refresh/issues/37)) ([1c06dd9](https://github.com/dantech2000/refresh/commit/1c06dd97f813f1e23cf20a8d96295b108c639a2c))
* **upgrade:** attach to in-flight addon updates on resume (REF-114) ([#48](https://github.com/dantech2000/refresh/issues/48)) ([780b097](https://github.com/dantech2000/refresh/commit/780b097560238e3c41e21c5d01c113698fa7fbc2))


### Code Refactoring

* consolidate duplicated table, timing, filter, pagination, and badge code ([#22](https://github.com/dantech2000/refresh/issues/22)) ([8788a28](https://github.com/dantech2000/refresh/commit/8788a28d43417dc7454acc7a317af4173576a1f4))
* logging, addon factory, batched ASG, scale-dry-run PDBs, split actions.go (REF-37, 39, 50, 4, 38) ([#39](https://github.com/dantech2000/refresh/issues/39)) ([03e857f](https://github.com/dantech2000/refresh/commit/03e857f5c0aa2215c202c9cc2df37f0a21d9a8ed))
* migrate CLI from urfave/cli v2 to v3 (REF-11) ([#27](https://github.com/dantech2000/refresh/issues/27)) ([19ea6e4](https://github.com/dantech2000/refresh/commit/19ea6e456a8e724a04a92c0c1209ac3d1dc8d5b6))
* **trim:** refocus as the EKS upgrade companion — remove diff, cost, utilization, workload pdbs (REF-78) ([#36](https://github.com/dantech2000/refresh/issues/36)) ([9c6ce30](https://github.com/dantech2000/refresh/commit/9c6ce30a408e7ffcd09a541f53ae6ff75c5b9bc7))

## [0.6.0](https://github.com/dantech2000/refresh/compare/v0.5.12...v0.6.0) (2026-06-06)


### Features

* **version:** support --version/-v flag in addition to version subcommand ([b3713de](https://github.com/dantech2000/refresh/commit/b3713de0db6129de00085416efefa75c2c53866e))


### Bug Fixes

* addon update version positional + addons semaphore ordering ([a7c09d2](https://github.com/dantech2000/refresh/commit/a7c09d260ced9ef3c24be772910b771d7c0b3b5f))
* **clusterview:** tree view preserves cluster status under unknown health ([f48da16](https://github.com/dantech2000/refresh/commit/f48da16fd754c9a4bbe4f48819386f4839135587))
* multi-region cache collision + addon resolver nil deref ([b08ca75](https://github.com/dantech2000/refresh/commit/b08ca75829b73d03e0e6530fb6c2f3ca66855acf))
* **nodegroup:** --health-only always prints verdict, even under --quiet ([9b242f1](https://github.com/dantech2000/refresh/commit/9b242f135c677caf4c531c6be357d641926bf3cb))


### Code Refactoring

* **addon:** adopt runner.SetupAWSStrict and PositionalAt ([6568846](https://github.com/dantech2000/refresh/commit/65688464eda6a0d25ffa1e782937cd957de6455f))
* **addons:** dedupe UpdateAll parallel/serial branches ([b666f8d](https://github.com/dantech2000/refresh/commit/b666f8d0bf987a5ce9a31fccc84bdf556fd677fe))
* **cluster:** clean ListAllRegions structure ([dc23fc3](https://github.com/dantech2000/refresh/commit/dc23fc374f7bbedc513177ba0fa7d373a87d7a31))
* **cluster:** collapse outputClustersTable's multiRegion×showHealth branches ([1e97291](https://github.com/dantech2000/refresh/commit/1e97291a16f954b6b39f92d17e237d5bc3bbde11))
* **cluster:** consolidate color/status formatters ([ea2e311](https://github.com/dantech2000/refresh/commit/ea2e3113293f754bcc1147d0d4a99ebd9cb09da4))
* **cluster:** extract diff helpers in analyzeDifferences ([4e202ab](https://github.com/dantech2000/refresh/commit/4e202ab61f3e103e1c21ec6b16b4cf00c4e4db8e))
* **cluster:** getClusterSummary drops always-nil error return ([d569c23](https://github.com/dantech2000/refresh/commit/d569c235ce5c0b06b7a0bc28ca055b91db5eb243))
* **cluster:** simplify buildListCacheKey ([6c575e3](https://github.com/dantech2000/refresh/commit/6c575e3555fa0a169480bc9ed9db8482860ede99))
* **clusterview:** split into color/list/detail/compare files ([a13f7c0](https://github.com/dantech2000/refresh/commit/a13f7c0e362857329768dd305ecf117cb9671e09))
* **commands:** extract clusterview pkg, finish runner adoption ([c9101ea](https://github.com/dantech2000/refresh/commit/c9101eab8ef789819f2912d3ece0d061192ed3ec))
* **commands:** extract runner package for shared CLI primitives ([f324738](https://github.com/dantech2000/refresh/commit/f324738cb4df132eceef0a3ab27f9e404f9fa8fc))
* **common:** add Paginate generic and migrate 5 ListX loops ([38d76a7](https://github.com/dantech2000/refresh/commit/38d76a7f4a2abd157d6a6b554edc5e9c42085835))
* **nodegroup:** adopt runner in runScale and runUpdateAMI ([7728468](https://github.com/dantech2000/refresh/commit/7728468833a92c1c0f7a6e253d8af8f78436d1d7))
* **nodegroup:** dedupe CloudWatch utilization collectors ([d8dda36](https://github.com/dantech2000/refresh/commit/d8dda364c6acf8416640fe58aa7be5ae3ea4fd66))
* **nodegroup:** extract classifyAMI helper ([617d693](https://github.com/dantech2000/refresh/commit/617d693f1e3e80e78ccd9ef0694734d7ecdb3ead))
* **nodegroup:** split runUpdateAMI into pipeline stages ([5848223](https://github.com/dantech2000/refresh/commit/58482230e39e7cc0532b47b0e683c203506a6aee))
* P3 dead code + helper consolidations ([ad9a24d](https://github.com/dantech2000/refresh/commit/ad9a24d3a65e1bb2dd9abf7912ae3249f63b3460))

## [0.5.12](https://github.com/dantech2000/refresh/compare/v0.5.11...v0.5.12) (2026-05-14)


### Bug Fixes

* seed release-please manifest at v0.5.11 (actual latest release) ([a8cac41](https://github.com/dantech2000/refresh/commit/a8cac41beb8278d36e5cb9a2a3d873b2af672624))
* use GH_PAT for release-please PR creation ([218215b](https://github.com/dantech2000/refresh/commit/218215b028a961b9da2d3cf459168caaa76168f7))


### Code Refactoring

* extract CheckAWSCredentials helper and add package godoc ([7ddd9d0](https://github.com/dantech2000/refresh/commit/7ddd9d0e0baad164133d576f08d9eee7c21ca53b))
* split internal/commands into focused sub-packages ([5d1a004](https://github.com/dantech2000/refresh/commit/5d1a0047772f37e30158fccd1365db7be4b48e33))
* split internal/commands into focused sub-packages ([fc4f785](https://github.com/dantech2000/refresh/commit/fc4f785dfc2fd29193dc62ef2a41d9a0ddf7ad0e))

## [0.5.1](https://github.com/dantech2000/refresh/compare/v0.5.0...v0.5.1) (2026-05-14)


### Bug Fixes

* add post-install xattr hook to remove macOS quarantine bit ([9933ffc](https://github.com/dantech2000/refresh/commit/9933ffcd8e400392a0ed7d57831678396384af2b))
* polish cluster and nodegroup workflows ([b7b3685](https://github.com/dantech2000/refresh/commit/b7b3685b67714d0fac1db1ddb318f73a9c021440))
* resolve golangci-lint issues ([899002e](https://github.com/dantech2000/refresh/commit/899002ee6fdf1b5dacd4e24127d8cfed6e6a3bb9))
* use GH_PAT for release-please PR creation ([218215b](https://github.com/dantech2000/refresh/commit/218215b028a961b9da2d3cf459168caaa76168f7))


### Code Refactoring

* extract CheckAWSCredentials helper and add package godoc ([7ddd9d0](https://github.com/dantech2000/refresh/commit/7ddd9d0e0baad164133d576f08d9eee7c21ca53b))
* split internal/commands into focused sub-packages ([5d1a004](https://github.com/dantech2000/refresh/commit/5d1a0047772f37e30158fccd1365db7be4b48e33))
* split internal/commands into focused sub-packages ([fc4f785](https://github.com/dantech2000/refresh/commit/fc4f785dfc2fd29193dc62ef2a41d9a0ddf7ad0e))
