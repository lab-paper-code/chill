package main

import (
 "encoding/json"
 "errors"
 "reflect"
 "testing"
 "time"

 api "github.com/lab-paper-code/chill/api/v1alpha1"
 "github.com/lab-paper-code/chill/internal/serving"
 "github.com/lab-paper-code/chill/internal/serving/httpapi"
 servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
 core "k8s.io/api/core/v1"
 meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type occupancyFixture struct {
 intent httpapi.OccupancyIntent
 policy dispatchOccupancyPolicy
 cm *core.ConfigMap
 profile *serving.Profile
 node *core.Node
 workload *api.Workload
 preprocessor *api.Preprocessor
 preparation servingkube.PrepareSpec
 timeout time.Duration
}

func syntheticOccupancyFixture() occupancyFixture {
 f:=occupancyFixture{intent:httpapi.OccupancyIntent{Node:"node",BatchEnergyOptimization:"beo",Workload:"workload",Batch:2},policy:syntheticOccupancyPolicy(),
  cm:&core.ConfigMap{ObjectMeta:meta.ObjectMeta{Namespace:"serving",Name:"scheduler",UID:"config-uid",ResourceVersion:"9"}},
  profile:&serving.Profile{ID:"allowed/beo/beo-uid",NodeName:"node",Batches:[]serving.BatchProfile{{Batch:2,DurationNanos:[]float64{10e6,20e6,30e6}}}},
  node:&core.Node{ObjectMeta:meta.ObjectMeta{Name:"node",UID:"node-uid"}},
  workload:&api.Workload{ObjectMeta:meta.ObjectMeta{Name:"workload",UID:"workload-uid",Generation:3},Spec:api.WorkloadSpec{
   ModelRef:api.CatalogReference{Name:"model"},PreprocessorRef:api.CatalogReference{Name:"preprocessor"},DatasetRef:&api.CatalogReference{Name:"dataset"},
   SLO:api.WorkloadSLO{Percentile:99,TargetLatency:meta.Duration{Duration:1500*time.Millisecond}}}},
  preprocessor:&api.Preprocessor{ObjectMeta:meta.ObjectMeta{Name:"preprocessor",UID:"preprocessor-uid",Generation:1},Spec:api.PreprocessorSpec{
   Input:api.RawInputContract{Fields:[]api.RawField{{Name:"text",Format:"UTF8",MaxBytes:1020}}}}},timeout:2*time.Second}
 f.profile.Condition.NodeUID="node-uid"
 f.profile.Condition.LogicalModel="model"
 f.profile.Condition.RuntimeImage="runtime@sha256:synthetic"
 f.profile.Condition.Preprocessor.Name="preprocessor"
 f.profile.Condition.Preprocessor.Inputs=[]string{"text"}
 f.preparation.Config.MaxQueueBytes=64<<20
 f.preparation.Config.MaxQueueRequests=128
 return f
}

func(f occupancyFixture) build()(*api.DispatchOccupancyMeasurement,error){
 return deriveOccupancy("allowed",f.intent,f.policy,f.cm,"scheduler.json",f.profile,f.node,f.workload,f.preprocessor,f.preparation,f.timeout)
}

func requireOccupancyError(t *testing.T, err error,code string){
 t.Helper();var domain *serving.Error
 if !errors.As(err,&domain)||domain.Code!=code {t.Fatalf("want %s, got %v",code,err)}
}

func TestOccupancyDerivationFreezesProtocolAndCompleteElapsedBudget(t *testing.T){
 f:=syntheticOccupancyFixture()
 task,err:=f.build();if err!=nil {t.Fatal(err)}
 if err:=task.Spec.ValidateConditions();err!=nil {t.Fatal(err)}
 if task.Spec.Workers!=33 || task.Spec.Conditions.WaitingBatches!=16 || task.Spec.Conditions.MinimumCycles!=3 {t.Fatalf("incorrect supply: %+v",task.Spec)}
 s:=task.Spec
 budget:=s.PreparationSeconds+s.JobSetupSeconds+s.CollectionSeconds+s.WarmupSeconds+s.DurationSeconds+s.RequestLifetimeSeconds+(s.DeliveryMarginMillis+999)/1000+s.DrainSeconds
 if budget!=s.PlannedElapsedSeconds {t.Fatalf("budget %d != E %d",budget,s.PlannedElapsedSeconds)}
 var inputs struct{
  DispatchOccupancy dispatchOccupancyPolicy `json:"dispatchOccupancy"`
  Configuration struct{Namespace,Name,UID,ResourceVersion,Key string} `json:"configuration"`
 }
 if err:=json.Unmarshal([]byte(s.Conditions.InputsJSON),&inputs);err!=nil {t.Fatal(err)}
 if !reflect.DeepEqual(inputs.DispatchOccupancy,f.policy)||inputs.Configuration.UID!="config-uid"||inputs.Configuration.ResourceVersion!="9"||inputs.Configuration.Key!="scheduler.json" {t.Fatal("policy/provenance not frozen")}
 again,err:=f.build();if err!=nil||!reflect.DeepEqual(task,again){t.Fatal("derivation is not deterministic",err)}
 f.policy.PlannedElapsedCapSeconds=float64(budget)
 atCap,err:=f.build();if err!=nil {t.Fatal("complete budget exactly at cap failed",err)}
 if err=atCap.Spec.ValidateConditions();err!=nil {t.Fatal(err)}
 f.policy.PlannedElapsedCapSeconds--
 _,err=f.build();requireOccupancyError(t,err,"OccupancyInfeasible")
 // Also exercise the unchanged stored-contract elapsed validator, not just builder arithmetic.
 var frozen map[string]any
 if err=json.Unmarshal([]byte(atCap.Spec.Conditions.InputsJSON),&frozen);err!=nil {t.Fatal(err)}
 frozen["coefficients"].(map[string]any)["plannedElapsedCapSeconds"]=float64(budget-1)
 raw,_:=json.Marshal(frozen);atCap.Spec.Conditions.InputsJSON=string(raw)
 if atCap.Spec.ValidateConditions()==nil {t.Fatal("stored validator accepted cap-1")}
}

func TestOccupancyDerivationRefusesLifetimeIdentityAndSupply(t *testing.T){
 for name,change:=range map[string]func(*occupancyFixture){
  "SLO ceil":func(f *occupancyFixture){f.workload.Spec.SLO.TargetLatency.Duration=2001*time.Millisecond},
  "actual gateway cap":func(f *occupancyFixture){f.timeout=1999*time.Millisecond},
  "node replacement":func(f *occupancyFixture){f.node.UID="replacement"},
  "wrong model":func(f *occupancyFixture){f.workload.Spec.ModelRef.Name="other"},
  "unbounded raw field":func(f *occupancyFixture){f.preprocessor.Spec.Input.Fields[0].MaxBytes=0},
  "missing batch":func(f *occupancyFixture){f.intent.Batch=3},
  "infeasible saturation":func(f *occupancyFixture){f.policy.RefillSeconds=10},
  "lifetime cannot hold batch":func(f *occupancyFixture){f.profile.Batches[0].DurationNanos=[]float64{1e9}},
  "byte supply cap":func(f *occupancyFixture){f.preprocessor.Spec.Input.Fields[0].MaxBytes=32<<20},
  "queue bound":func(f *occupancyFixture){f.preparation.Config.MaxQueueRequests=1},
 } {t.Run(name,func(t *testing.T){f:=syntheticOccupancyFixture();change(&f);_,err:=f.build();requireOccupancyError(t,err,"OccupancyInfeasible")})}
 f:=syntheticOccupancyFixture();f.workload.Spec.SLO.TargetLatency.Duration=2*time.Second
 if _,err:=f.build();err!=nil {t.Fatal("inclusive lifetime/SLO/gateway boundary",err)}
}
