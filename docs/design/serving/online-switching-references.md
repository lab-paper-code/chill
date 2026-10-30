# 전환 비용을 고려한 온라인 구성 선택: 레퍼런스

상태: reference — 아래 문헌은 정책 채택이 아니다. 현행 판정·고정 H는 [용량표 계획기](analytic-planner.md), 요구사항은 [구성 비용](../../spec/04_configuration_selection.md)·[전환 실행](../../spec/05_transition_control.md)이 권위다.
| 문헌·읽을 곳 | 적용 한계 |
|---|---|
| Azar, Feige, Nath (2007), [Two State WFA](https://www.microsoft.com/en-us/research/wp-content/uploads/2016/02/tr-2007-20.pdf), §2/§4/정리4.1 | 현재 상대비용을 누적·차감하고 문턱은 양방향 고정 전환비 합. 두 상태의 현재 비용을 아는 즉시 전환 모형이며 준비·큐·SLO 보장이 아님 |
| Bubeck, Rabani (2019), [Parametrized MTS](https://arxiv.org/pdf/1904.03874), §2/정리1; 원전 [Borodin, Linial, Saks (1992)](https://doi.org/10.1145/146585.146588) | WFA는 상태별 과거 최소비용을 관리. 공개된 현재 비용벡터와 대칭·삼각부등식 metric을 전제하므로 방향별·상태의존 CHILL 전환에 보장을 이전하지 않음 |
| Lotker, Patt-Shamir, Rawitz (2008), [Multislope Ski Rental](https://arxiv.org/pdf/0802.2832), §1–2 | 누적 임대료=구매비 직관은 불가역 구매용; 반복 전환·준비시간 문턱을 직접 정하지 않음 |
| Rawlings, Mayne, Diehl, [MPC 2판](https://sites.engineering.ucsb.edu/~jbraw/mpc/), §2.2/§2.4; Tvedt (2022), [Entry/Exit](https://link.springer.com/article/10.1007/s43069-022-00161-9), §1–3 | 짧은 H는 장기 절감을 놓칠 수 있음. 유지/단일 전환 비교가 일반 MPC 보장은 아니며 확률적 전환에는 미래법칙·할인 가정이 필요 |
- 우선 검토는 반복 전환 task-system/WFA; 고정 H 재판단이 비교 기준이다. 임의 누적·초기화에 기존 보장을 붙이거나 미래 부하 예측을 전제하지 않는다.
- 채택 전 profile 추정과 정확한 현재 비용, 준비/임시 용량/큐/진행/취소를 구분하고 SLO·실행가능성 우선과 동일 시간축 전체 POOL J를 유지한다.
- 오프라인 최적 비교자는 미래 정보만 추가하며 초기 상태·전환시간·자원·SLO는 같아야 한다. 경쟁비는 실측 절감률이 아니다. 다음 산출물은 관측 입력·유지/전환 식·전환 중 재판단 규칙과 짧은/지속/반복 부하의 검증이다.
