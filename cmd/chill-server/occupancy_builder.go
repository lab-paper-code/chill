package main

import (
 "context"
 "crypto/sha256"
 "encoding/json"
 "fmt"
 "math"
 "slices"
 "strconv"
 "strings"
 "time"

 api "github.com/lab-paper-code/chill/api/v1alpha1"
 "github.com/lab-paper-code/chill/internal/serving"
 "github.com/lab-paper-code/chill/internal/serving/httpapi"
 servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
 core "k8s.io/api/core/v1"
 meta "k8s.io/apimachinery/pkg/apis/meta/v1"
 "sigs.k8s.io/controller-runtime/pkg/client"
)

func occupancyInfeasible(reason string) error {
 return &serving.Error{Code:"OccupancyInfeasible", Message:reason}
}

func occupancyPolicyUnavailable(reason string) error {
 return &serving.Error{Code:"OccupancyPolicyUnavailable", Message:reason}
}

// buildOccupancy reads one fresh configuration and the scheduler's accepted profile
// under its mutation lock. A broken/missing policy never reuses an earlier policy.
func (o *measurementService) buildOccupancy(ctx context.Context, namespace string, intent httpapi.OccupancyIntent) (*api.DispatchOccupancyMeasurement, error) {
 if err:=o.namespace(namespace); err!=nil {return nil,err}
 if o.scheduler==nil {return nil,occupancyPolicyUnavailable("scheduler configuration is unavailable")}
 s:=o.scheduler
 s.lock.Lock()
 defer s.lock.Unlock()
 cm,err:=o.kube.CoreV1().ConfigMaps(o.opts.servingNamespace).Get(ctx,o.opts.schedulerConfigMap,meta.GetOptions{})
 if err!=nil {return nil,occupancyPolicyUnavailable("scheduler ConfigMap is unavailable")}
 config,err:=decodeSchedulerConfiguration(cm.Data[o.opts.schedulerConfigKey],o.opts.maxRequestBytes)
 if err!=nil {return nil,occupancyPolicyUnavailable(err.Error())}
 if config.DispatchOccupancy==nil {return nil,occupancyPolicyUnavailable("dispatchOccupancy block is required")}
 published:=s.profiles.Load()
 if published==nil || published.ConfigVersion!=cm.ResourceVersion {return nil,occupancyPolicyUnavailable("scheduler has not accepted this configuration revision")}
 beo:=&api.BatchEnergyOptimization{}
 if err=o.client.Get(ctx,client.ObjectKey{Namespace:namespace,Name:intent.BatchEnergyOptimization},beo);err!=nil {return nil,err}
 if beo.Status.Phase!="Complete" || beo.DeletionTimestamp!=nil || beo.Spec.State.TargetNode!=intent.Node {
  return nil,occupancyInfeasible("BEO must be Complete on the selected Node")
 }
 id:=namespace+"/"+beo.Name+"/"+string(beo.UID)
 index:=slices.IndexFunc(published.Profiles,func(p serving.Profile)bool{
  return p.ID==id || p.Reuse!=nil && p.Reuse.Outcome=="Accepted" && p.Reuse.ConfirmationProfileID==id
 })
 if index<0 {return nil,occupancyInfeasible("BEO has no current accepted profile")}
 profile:=&published.Profiles[index]
 preparation,ok:=published.Preparations[profile.ID]
 if !ok {return nil,occupancyInfeasible("selected profile has no accepted native preparation")}
 node,err:=o.kube.CoreV1().Nodes().Get(ctx,intent.Node,meta.GetOptions{})
 if err!=nil {return nil,err}
 workload:=&api.Workload{}
 if err=o.client.Get(ctx,client.ObjectKey{Name:intent.Workload},workload);err!=nil {return nil,err}
 preprocessor:=&api.Preprocessor{}
 if err=o.client.Get(ctx,client.ObjectKey{Name:workload.Spec.PreprocessorRef.Name},preprocessor);err!=nil {return nil,err}
 return deriveOccupancy(namespace,intent,*config.DispatchOccupancy,cm,o.opts.schedulerConfigKey,profile,node,workload,preprocessor,preparation,s.gatewayRequestTimeout)
}

func deriveOccupancy(namespace string, intent httpapi.OccupancyIntent, policy dispatchOccupancyPolicy,
 cm *core.ConfigMap, key string, profile *serving.Profile, node *core.Node, workload *api.Workload,
 preprocessor *api.Preprocessor, preparation servingkube.PrepareSpec, gatewayTimeout time.Duration,
) (*api.DispatchOccupancyMeasurement,error) {
 if node.UID=="" || string(node.UID)!=profile.Condition.NodeUID || profile.NodeName!=node.Name || intent.Node!=node.Name {
  return nil,occupancyInfeasible("current Node UID differs from the measured profile")
 }
 if workload.UID=="" || workload.Spec.DatasetRef==nil || workload.Spec.ModelRef.Name!=profile.Condition.LogicalModel ||
  workload.Spec.PreprocessorRef.Name!=profile.Condition.Preprocessor.Name || preprocessor.Name!=workload.Spec.PreprocessorRef.Name {
  return nil,occupancyInfeasible("Workload model, preprocessor and dataset must resolve the selected profile")
 }
 lifetime:=float64(policy.RequestLifetimeSeconds)
 if workload.Spec.SLO.TargetLatency.Duration<=0 || lifetime<math.Ceil(workload.Spec.SLO.TargetLatency.Duration.Seconds()) ||
  gatewayTimeout<=0 || time.Duration(policy.RequestLifetimeSeconds)*time.Second>gatewayTimeout {
  return nil,occupancyInfeasible("request lifetime must cover ceil(Workload SLO) and not exceed the actual gateway request timeout")
 }
 var population []float64
 for _,batch:=range profile.Batches {if batch.Batch==int(intent.Batch) {population=batch.DurationNanos;break}}
 if len(population)==0 {return nil,occupancyInfeasible("selected batch has no measured service population")}
 fast,tail,sum:=math.Inf(1),0.0,0.0
 for _,nanos:=range population {
  seconds:=nanos/1e9
  if seconds<=0 || math.IsNaN(seconds) || math.IsInf(seconds,0) {return nil,occupancyInfeasible("profile batch times must be finite and positive")}
  fast,tail,sum=math.Min(fast,seconds),math.Max(tail,seconds),sum+seconds
 }
 mean:=sum/float64(len(population))
 if intent.Batch<1 || math.IsInf(mean,0) {return nil,occupancyInfeasible("invalid batch population")}
 // firstfloor stores each decoded raw field with a four-byte length prefix.
 var requestBytes int64
 if len(preprocessor.Spec.Input.Fields)==0 || len(preprocessor.Spec.Input.Fields)!=len(profile.Condition.Preprocessor.Inputs) {
  return nil,occupancyInfeasible("current raw input contract differs from the profile")
 }
 for i,field:=range preprocessor.Spec.Input.Fields {
  if field.MaxBytes<1 || field.Name!=profile.Condition.Preprocessor.Inputs[i] {return nil,occupancyInfeasible("bounded matching raw input fields required")}
  requestBytes+=int64(field.MaxBytes)+4
 }
 // These are the existing native supplySizing contract caps, not scientific defaults.
 const workerCap=64
 const queueBytesCap=64<<20
 if preparation.Config.MaxQueueBytes<queueBytesCap || preparation.Config.MaxQueueRequests<1 || requestBytes>queueBytesCap {
  return nil,occupancyInfeasible("native queue cannot support the existing 64/64MiB supply contract")
 }
 margin:=float64(policy.DeliveryMarginMillis)/1000
 workers,bound:=float64(workerCap),"queue-cap"
 if bytesWorkers:=float64(queueBytesCap/requestBytes);bytesWorkers<workers {workers,bound=bytesWorkers,"bytes-cap"}
 if lifetimeWorkers:=math.Floor((lifetime-margin)/(float64(intent.Batch)*tail));lifetimeWorkers<workers {workers,bound=lifetimeWorkers,"lifetime"}
 if workers<float64(intent.Batch) {return nil,occupancyInfeasible("lifetime or decoded-byte cap cannot hold a complete batch")}
 q:=math.Ceil(workers/float64(intent.Batch))-1
 waiting:=int64(intent.Batch)*int64(q)
 if waiting>int64(preparation.Config.MaxQueueRequests) || waiting>queueBytesCap/requestBytes {
  return nil,occupancyInfeasible("profile-derived outstanding supply exceeds native queue capacity")
 }
 // Conservatively require full queued batches to hide the configured refill bound.
 // Residual idle I=max(0,F-floor((K-B)/B)*t_fast); I/(t_fast+I)<=idle target.
 queuedBatches:=math.Floor((workers-float64(intent.Batch))/float64(intent.Batch))
 residual:=math.Max(0,policy.RefillSeconds-queuedBatches*fast)
 estimatedIdle:=residual/(fast+residual)
 if estimatedIdle>policy.MaximumIdleFraction {
  return nil,occupancyInfeasible(fmt.Sprintf("safe supply %.0f workers cannot saturate: estimated idle %.6g exceeds %.6g (refill bound %.6gs)",workers,estimatedIdle,policy.MaximumIdleFraction,policy.RefillSeconds))
 }
 warmup:=math.Ceil(math.Max(policy.LoadPreparationFloorSeconds,(q+1)*tail+policy.RefillSeconds))
 duration:=math.Ceil(math.Max(policy.WindowFloorSeconds,policy.TimeMargin*float64(policy.MinimumBatches)*mean))
 drain:=math.Ceil(lifetime+tail+margin)
 extension:=int64(policy.RequestLifetimeSeconds)+(int64(policy.DeliveryMarginMillis)+999)/1000
 // Reserve supplyExtension BEFORE admitting job/collection allowances; never pad E.
 known:=float64(policy.PreparationSeconds)+warmup+duration+float64(extension)+drain
 elapsed:=known+float64(policy.JobSetupSeconds)+float64(policy.CollectionSeconds)
 if math.IsInf(elapsed,0) || elapsed>math.Floor(policy.PlannedElapsedCapSeconds) || elapsed>math.MaxInt32 {
  return nil,occupancyInfeasible("elapsed cap cannot hold preparation, warmup, duration, supply extension, drain and job/collection allowances")
 }
 spec:=api.DispatchOccupancyMeasurementSpec{
  Node:intent.Node,BatchEnergyOptimization:intent.BatchEnergyOptimization,Workload:intent.Workload,Batch:intent.Batch,
  SupplyMode:policy.SupplyMode,Seed:policy.Seed,MinimumBatches:policy.MinimumBatches,
  MaximumIdleFraction:strconv.FormatFloat(policy.MaximumIdleFraction,'f',-1,64),Workers:int32(workers),
  RequestLifetimeSeconds:policy.RequestLifetimeSeconds,DeliveryMarginMillis:policy.DeliveryMarginMillis,
  PreparationSeconds:policy.PreparationSeconds,JobSetupSeconds:policy.JobSetupSeconds,CollectionSeconds:policy.CollectionSeconds,
  WarmupSeconds:int32(warmup),DurationSeconds:int32(duration),DrainSeconds:int32(drain),PlannedElapsedSeconds:int32(elapsed),
  Conditions:api.DispatchOccupancyConditions{ProfileID:profile.ID,WaitingBatches:int32(q),MinimumCycles:policy.MinimumBatches-1,
   MaximumIdleNanosPerRequest:int64(math.Floor(policy.IdleDErrorSecondsPerRequest*1e9))},
 }
 inputs:=map[string]any{
  "dispatchOccupancy":policy,
  "configuration":map[string]any{"namespace":cm.Namespace,"name":cm.Name,"uid":cm.UID,"resourceVersion":cm.ResourceVersion,"key":key},
  "node":serving.NodeReference{Name:node.Name,UID:string(node.UID)},
  "coefficients":policy,
  "profile":map[string]any{"profileID":profile.ID,"runtimeImage":profile.Condition.RuntimeImage,"reference":profile.Reference,"evidence":profile.Evidence,
   "batch":intent.Batch,"durationNanos":population,"meanBatchSeconds":mean,"maxObservedSeconds":tail,
   "workload":map[string]any{"name":workload.Name,"uid":workload.UID,"generation":workload.Generation,
    "slo":map[string]any{"percentile":workload.Spec.SLO.Percentile,"targetLatencyNanos":workload.Spec.SLO.TargetLatency.Duration.Nanoseconds()}},
   "preprocessor":map[string]any{"name":preprocessor.Name,"uid":preprocessor.UID,"generation":preprocessor.Generation,"input":preprocessor.Spec.Input}},
  "computed":map[string]any{"tailBatchSeconds":tail,"fastBatchSeconds":fast,"prefixSeconds":margin,"refillSeconds":policy.RefillSeconds,
   "framedDecodedRequestUpperBytes":requestBytes,"queueRequestCap":preparation.Config.MaxQueueRequests,"queueBytesCap":queueBytesCap,
   "qUpper":preparation.Config.MaxQueueRequests/int(intent.Batch),"estimatedIdleFraction":estimatedIdle,"supplyExtensionSeconds":extension,
   "supplySizing":map[string]any{"batch":intent.Batch,"profileMaxBatchSeconds":tail,"serviceTimeSource":"profile.maxObservedSeconds",
    "requestLifetimeSeconds":policy.RequestLifetimeSeconds,"marginSeconds":margin,"workerCap":workerCap,"queueBytesCap":queueBytesCap,
    "framedDecodedRequestUpperBytes":requestBytes,"limitingBound":bound}},
 }
 raw,err:=json.Marshal(inputs)
 if err!=nil {return nil,occupancyInfeasible(err.Error())}
 spec.Conditions.InputsJSON=string(raw)
 // ValidateConditions includes validateElapsedBudget: validate the final object once.
 if err:=spec.ValidateConditions();err!=nil {return nil,occupancyInfeasible(err.Error())}
 name:=intent.Name
 if name=="" {
  frozen,_:=json.Marshal(spec)
  hash:=sha256.Sum256(frozen)
  nodePart:=strings.TrimRight(intent.Node[:min(len(intent.Node),40)],"-.")
  beoPart:=strings.TrimRight(intent.BatchEnergyOptimization[:min(len(intent.BatchEnergyOptimization),40)],"-.")
  name=fmt.Sprintf("occ-%s-%s-b%d-%x",nodePart,beoPart,intent.Batch,hash[:5])
 }
 return &api.DispatchOccupancyMeasurement{TypeMeta:meta.TypeMeta{APIVersion:api.GroupVersion.String(),Kind:"DispatchOccupancyMeasurement"},
  ObjectMeta:meta.ObjectMeta{Namespace:namespace,Name:name},Spec:spec},nil
}
