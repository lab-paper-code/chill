import assert from "node:assert/strict"
import test from "node:test"
import { readFileSync } from "node:fs"
import { capacityKappaHat, capacityPath, capacityTests, capacityWitness, planSearchFacts, type CapacityView } from "./capacity.ts"
import { parseSchedulerJSON } from "./scheduler.ts"

const fixture=readFileSync(new URL("./fixtures/scheduler-feedback-capacity.json",import.meta.url),"utf8")

test("HTTP projection fixture preserves tested failure, untested zero and witnessed pass",()=>{
  const response=parseSchedulerJSON(fixture) as CapacityView
  assert.deepEqual(Object.keys(response),["tests"])
  const [failed,untested,passed]=response.tests!
  assert.equal(failed.kappaHat,0)
  assert.equal(untested.kappaHat,0)
  assert.equal(capacityWitness(failed),"검사한 요청률 모두 미충족 · 통과 근거 없음")
  assert.equal(capacityWitness(untested),"미검사 · 통과 근거 없음")
  assert.equal(capacityWitness({...untested,tests:null}),capacityWitness(untested))
  assert.equal(capacityWitness(passed),"검사한 요청률에서 통과 근거 있음")
  assert.notEqual(capacityKappaHat(failed),capacityKappaHat(untested))
  assert.equal(capacityKappaHat(undefined),"미기록")
  assert.equal(failed.tests![0].inputKey,"failed input / +&")
  assert.deepEqual(failed.tests![0],{inputKey:"failed input / +&",rate:40,met:false,passed:0,requests:100})
  assert.equal(failed.input.allocation.memoryBytes,9007199254740993n)
  assert.equal(failed.input.profileID,"profile-id")
  assert.equal(failed.input.powerMode,"15W")
  assert.deepEqual(failed.pathFeedback,{workloadUID:"service-uid",percentile:99,totalNanos:40000000,postNanos:30000000,sampleCount:1,source:"empirical"})
  assert.equal(untested.pathFeedback.totalNanos,0)
  assert.equal(untested.pathFeedback.postNanos,0)
  assert.equal(Object.hasOwn(failed,"option"),false)
  assert.equal(passed.kappaHat,12.5)
  assert.deepEqual(passed.tests!.map(test=>test.rate),[12.5,20])
})

test("current evidence filters exact input workload identity and keeps empty diagnostics",()=>{
  const response=parseSchedulerJSON(fixture) as CapacityView,tests=response.tests!
  assert.deepEqual(capacityTests(tests,""),tests)
  assert.deepEqual(capacityTests(tests,"service-uid"),tests)
  assert.deepEqual(capacityTests(tests,"absent-uid"),[])
  assert.deepEqual(capacityTests([],"service-uid"),[])
  assert.deepEqual((parseSchedulerJSON('{"tests":[]}') as CapacityView).tests,[])
  const path=capacityPath("uid with / space")
  assert.deepEqual(Object.fromEntries(new URLSearchParams(path.split("?")[1])),{evidence:"capacity",workload:"uid with / space"})
  assert.equal(capacityPath(),"/?evidence=capacity")
})

test("plan search termination reasons distinguish evaluation budget from time deadline",()=>{
  const search={neighborhood:3,evaluated:40,durationNanos:1000}
  const historical=planSearchFacts(search)
  assert.equal(historical.some(([label])=>label.includes("종료")),false)
  for(const [terminationReason,expected] of [["noImprovement","개선 없음"],["evaluationBudget","평가 수 예산"],["deadline","시간 마감"],["budgetCut","시간 마감"],["futureReason","futureReason"]]){
    const facts=planSearchFacts({...search,terminationReason,budgetCut:false})
    assert.deepEqual(facts.find(([label])=>label==="탐색 종료 사유"),["탐색 종료 사유",expected])
    assert.deepEqual(facts.find(([label])=>label==="시간 마감 종료"),["시간 마감 종료","아니요"])
    assert.equal(facts.some(([label])=>label==="예산 종료"),false)
  }
  assert.deepEqual(planSearchFacts({...search,budgetCut:true}).find(([label])=>label==="시간 마감 종료"),["시간 마감 종료","예"])
})
