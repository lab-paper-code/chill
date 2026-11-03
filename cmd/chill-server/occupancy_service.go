package main

import (
 "context"
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "errors"
 "strings"

 api "github.com/lab-paper-code/chill/api/v1alpha1"
 "github.com/lab-paper-code/chill/internal/evidencestore"
 "github.com/lab-paper-code/chill/internal/serving"
 "github.com/lab-paper-code/chill/internal/serving/httpapi"
 servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
 core "k8s.io/api/core/v1"
 apierrors "k8s.io/apimachinery/pkg/api/errors"
 meta "k8s.io/apimachinery/pkg/apis/meta/v1"
 "k8s.io/apimachinery/pkg/runtime/schema"
 "sigs.k8s.io/controller-runtime/pkg/client"
)

func(o *measurementService) submitOccupancy(ctx context.Context,task api.DispatchOccupancyMeasurement,dryRun bool)(httpapi.OccupancyMutationResult,error){
 if err:=o.namespace(task.Namespace);err!=nil {return httpapi.OccupancyMutationResult{},err}
 result:=httpapi.OccupancyMutationResult{Task:&task,Request:"Accepted"}
 if dryRun {
  // The final validated frozen object is returned without even a native create.
  result.Request="NOT_SUBMITTED"
  return result,nil
 }
 if err:=o.client.Create(ctx,&task);err!=nil {return httpapi.OccupancyMutationResult{},err}
 return result,nil
}

func(o *measurementService) deleteOccupancy(ctx context.Context,namespace,name,uid,version string)(httpapi.OccupancyMutationResult,error){
 result:=httpapi.OccupancyMutationResult{}
 if err:=o.namespace(namespace);err!=nil {return result,err}
 if uid==""||version=="" {return result,&serving.Error{Code:"ResourceVersionRequired",Message:"native UID and resourceVersion are required"}}
 if o.scheduler!=nil {o.scheduler.lock.Lock();defer o.scheduler.lock.Unlock()}
 task:=&api.DispatchOccupancyMeasurement{}
 if err:=o.client.Get(ctx,client.ObjectKey{Namespace:namespace,Name:name},task);err!=nil{return result,err}
 result.Task=task
 if string(task.UID)!=uid || task.ResourceVersion!=version {
  return result,apierrors.NewConflict(schema.GroupResource{Group:api.GroupVersion.Group,Resource:"dispatchoccupancymeasurements"},name,errors.New("native UID or resourceVersion changed"))
 }
 if task.Status.Phase==api.MeasurementPhaseMeasuring {return result,measurementInUse("a Measuring occupancy measurement cannot be deleted")}
 if err:=o.occupancyLoadStopped(ctx,task);err!=nil {return result,err}
 location,err:=o.occupancyOwnedEvidence(ctx,task)
 if err!=nil {return result,err}
 if location!=nil {
  if err:=o.objects.Delete(ctx,*location);err!=nil && !errors.Is(err,evidencestore.ErrNotFound) {
   return result,&serving.Error{Code:"MeasurementEvidenceDeletionIncomplete",Message:"owned occupancy evidence cleanup failed; native measurement retained"}
  }
  result.DeletedEvidence=[]httpapi.MeasurementEvidenceLocation{{Store:location.Store,Key:location.Key}}
 }
 result.EvidenceDeleted=true
 policy:=meta.DeletePropagationBackground
 if err:=o.client.Delete(ctx,task,&client.DeleteOptions{Preconditions:&meta.Preconditions{UID:&task.UID,ResourceVersion:&task.ResourceVersion},PropagationPolicy:&policy});err!=nil {
  return result,&serving.Error{Code:"MeasurementDeletionIncomplete",Message:"owned occupancy evidence cleaned but native deletion was not confirmed; query UID/resourceVersion before retrying"}
 }
 result.Request="DeletionRequested"
 return result,nil
}

// A load Job is named by the producer's hash of this DOM UID, not by Node alone.
func(o *measurementService) occupancyLoadStopped(ctx context.Context,task *api.DispatchOccupancyMeasurement)error{
 sum:=sha256.Sum256([]byte(task.UID))
 name:="occupancy-"+hex.EncodeToString(sum[:10])
 namespace:=o.opts.loadGenerator.Namespace
 if namespace=="" {namespace=o.opts.servingNamespace}
 job,err:=o.kube.BatchV1().Jobs(namespace).Get(ctx,name,meta.GetOptions{})
 if err!=nil && !apierrors.IsNotFound(err) {return err}
 jobUID:=""
 if err==nil {
  if !measurementJobTerminal(job) {return measurementInUse("the occupancy load Job is not terminal")}
  jobUID=string(job.UID)
 }
 pods,err:=o.kube.CoreV1().Pods(namespace).List(ctx,meta.ListOptions{})
 if err!=nil {return err}
 for _,pod:=range pods.Items {
  owner:=meta.GetControllerOf(&pod)
  if owner!=nil && owner.Kind=="Job" && owner.Name==name && (jobUID=="" || string(owner.UID)==jobUID) &&
   pod.Status.Phase!=core.PodSucceeded && pod.Status.Phase!=core.PodFailed {
   return measurementInUse("an occupancy load Pod remains active")
  }
 }
 return nil
}

func(o *measurementService) occupancyOwnedEvidence(ctx context.Context,task *api.DispatchOccupancyMeasurement)(*evidencestore.ObjectLocation,error){
 if o.objects==nil || o.opts.storeName=="" || o.opts.maxEvidenceBytes<1 {
  return nil,&serving.Error{Code:"MeasurementEvidenceUnavailable",Message:"bounded occupancy Evidence Store access is required for deletion"}
 }
 if task.UID=="" || strings.ContainsAny(string(task.UID),"/\\") {return nil,measurementInUse("invalid occupancy UID for owned evidence")}
 var frozen struct{Node serving.NodeReference `json:"node"`}
 if task.Spec.Conditions.InputsJSON!="" {
  if err:=json.Unmarshal([]byte(task.Spec.Conditions.InputsJSON),&frozen);err!=nil {return nil,measurementInUse("unreadable frozen occupancy Node identity")}
 }
 if frozen.Node.UID=="" {
  node,err:=o.kube.CoreV1().Nodes().Get(ctx,task.Spec.Node,meta.GetOptions{})
  if err!=nil {return nil,err}
  frozen.Node=serving.NodeReference{Name:node.Name,UID:string(node.UID)}
 }
 if frozen.Node.Name!=task.Spec.Node || frozen.Node.UID=="" || strings.ContainsAny(frozen.Node.UID,"/\\") {return nil,measurementInUse("invalid frozen occupancy Node identity")}
 location:=o.measurementLocation(servingkube.DispatchOccupancyPrefix+frozen.Node.UID+"/"+string(task.UID)+".json")
 if ref:=task.Status.Result;ref!=nil && (ref.Store!=location.Store || ref.Key!=location.Key || ref.SchemaVersion!=servingkube.DispatchOccupancySchema) {
  return nil,measurementInUse("occupancy result is outside its producer-owned evidence location")
 }
 raw,err:=o.readDeletionEvidence(ctx,location,true)
 if err!=nil {return nil,err}
 if raw==nil {return nil,nil}
 var record servingkube.DispatchOccupancyRecord
 if json.Unmarshal(raw,&record)!=nil || record.SchemaVersion!=servingkube.DispatchOccupancySchema || record.Node!=frozen.Node ||
  record.Conditions.Node!=task.Spec.Node || record.Conditions.BatchEnergyOptimization!=task.Spec.BatchEnergyOptimization ||
  record.Conditions.Workload!=task.Spec.Workload || record.Conditions.Batch!=task.Spec.Batch || record.ProfileID!=task.Spec.Conditions.ProfileID {
  return nil,measurementInUse("occupancy evidence does not describe this native measurement")
 }
 // Foreign DOM references must not grant deletion authority over shared evidence.
 tasks:=&api.DispatchOccupancyMeasurementList{}
 if err:=o.client.List(ctx,tasks);err!=nil {return nil,err}
 for _,other:=range tasks.Items {
  if other.UID==task.UID {continue}
  if ref:=other.Status.Result;ref!=nil && ref.Store==location.Store && ref.Key==location.Key {return nil,measurementInUse("occupancy evidence is referenced by another measurement")}
 }
 return &location,nil
}
