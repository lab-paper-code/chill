package main

import (
 "context"
 "fmt"
 "io/fs"
 "math"
 "path/filepath"
 "runtime/metrics"
 "strings"
 "sync"
 "time"

 "github.com/lab-paper-code/chill/internal/serving"
 corev1 "k8s.io/api/core/v1"
 metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
 "k8s.io/client-go/kubernetes"
)

type resourceReceiver struct {
 kube kubernetes.Interface
 log *serving.ObservationLog
 namespace string
 selectPod func(*corev1.Pod) bool
 maxSamples, maxContainers, maxThermal, maxErrors int
 mu sync.Mutex
 issued map[serving.NodeReference]map[serving.ResourceTarget]bool
}

// resourcePodSelected confines serving Pods to this system's namespace and
// control Pods to an explicitly configured release/component set.
func resourcePodSelected(pod *corev1.Pod, namespace, release string, components []string) bool {
 if pod.Namespace!=namespace {return false}
 component:=pod.Labels["app.kubernetes.io/component"]
 if component=="serving-instance" {return pod.Annotations["edge.dacs.io/serving-profile"]!=""}
 if release=="" || pod.Labels["app.kubernetes.io/instance"]!=release {return false}
 for _, allowed:=range components {if component==allowed {return true}}
 return false
}

func (s *resourceReceiver) receive(ctx context.Context, batch serving.ResourceBatch) ([]serving.ResourceTarget,error) {
 invalid:=func(message string) ([]serving.ResourceTarget,error) {return nil,&serving.Error{Code:"InvalidBody",Message:message,Field:"resources"}}
 if batch.Node.Name=="" || batch.Node.UID=="" || len(batch.Samples)>s.maxSamples {return invalid("node identity and bounded samples are required")}
 node,err:=s.kube.CoreV1().Nodes().Get(ctx,batch.Node.Name,metav1.GetOptions{});if err!=nil{return nil,err}
 if string(node.UID)!=batch.Node.UID {return invalid("node UID does not match current node")}
 s.mu.Lock();defer s.mu.Unlock()
 previous:=s.issued[batch.Node]
 if len(batch.Samples)>0 && previous==nil {return invalid("resource handshake required after server start")}
 for _,sample:=range batch.Samples {
  if sample.SampledAt.IsZero() || sample.SampledAt.Year()<1 || sample.SampledAt.Year()>9999 || len(sample.Containers)>s.maxContainers || len(sample.Thermal)>s.maxThermal || len(sample.Errors)>s.maxErrors {return invalid("invalid resource sample time or array bounds")}
  seen:=map[serving.ResourceTarget]bool{}
  for _,container:=range sample.Containers {
   if !previous[container.ResourceTarget] || seen[container.ResourceTarget] || (container.CgroupVersion!=1 && container.CgroupVersion!=2) || len(container.Errors)>s.maxErrors {return invalid("container identity was not issued, is duplicated, or has invalid cgroup version/errors")}
   seen[container.ResourceTarget]=true
   for _,failure:=range container.Errors {if failure.Target=="" || failure.Field=="" || failure.Reason=="" {return invalid("resource errors require target, field and reason")}}
  }
  for _,sensor:=range sample.Thermal {if strings.TrimSpace(sensor.Sensor)=="" {return invalid("thermal sensor identity is required")}}
  for _,failure:=range sample.Errors {if failure.Target=="" || failure.Field=="" || failure.Reason=="" {return invalid("resource errors require target, field and reason")}}
 }
 pods,err:=s.kube.CoreV1().Pods(s.namespace).List(ctx,metav1.ListOptions{FieldSelector:"spec.nodeName="+batch.Node.Name});if err!=nil{return nil,err}
 targets:=[]serving.ResourceTarget{};next:=map[serving.ResourceTarget]bool{}
 for i:=range pods.Items {
  pod:=&pods.Items[i];if pod.UID=="" || !s.selectPod(pod) {continue}
  statuses:=append(append([]corev1.ContainerStatus{},pod.Status.InitContainerStatuses...),pod.Status.ContainerStatuses...)
  for _,status:=range statuses {if status.ContainerID=="" || status.State.Running==nil {continue};target:=serving.ResourceTarget{PodUID:string(pod.UID),ContainerID:status.ContainerID};if !next[target] {next[target]=true;targets=append(targets,target)}}
 }
 if len(targets)>s.maxContainers {return nil,fmt.Errorf("resource targets exceed configured container bound")}
 if len(batch.Samples)>0 {
  received:=time.Now().UTC()
  if err:=s.log.ResourcesRecord(serving.ResourceObservation{Kind:"node",Node:&batch.Node,ReceivedAt:&received,Samples:batch.Samples});err!=nil{return nil,err}
 }
 if s.issued==nil {s.issued=map[serving.NodeReference]map[serving.ResourceTarget]bool{}}
 s.issued[batch.Node]=next
 return targets,nil
}

type processResources struct {
 log *serving.ObservationLog
 directory,podUID string
 started time.Time
 directoryInterval time.Duration
 nextDirectory time.Time
 samples []metrics.Sample
}
func newProcessResources(log *serving.ObservationLog, directory,podUID string, started time.Time, directoryInterval time.Duration) *processResources {
 return &processResources{log:log,directory:directory,podUID:podUID,started:started,directoryInterval:directoryInterval,samples:[]metrics.Sample{{Name:"/cpu/classes/gc/total:cpu-seconds"},{Name:"/gc/cycles/total:gc-cycles"}}}
}
func (p *processResources) record(at time.Time) error {
 metrics.Read(p.samples)
 cpu,cycles:=p.samples[0].Value.Float64(),p.samples[1].Value.Uint64()
 if math.IsNaN(cpu)||math.IsInf(cpu,0) {return fmt.Errorf("invalid GC CPU metric")}
 row:=serving.ResourceObservation{Kind:"server",At:&at,PodUID:p.podUID,ProcessStartedAt:&p.started,GCCPUSeconds:&cpu,GCCycles:&cycles}
 if !at.Before(p.nextDirectory) {
  row.DirectoryObservedAt=&at
  var total int64
  err:=filepath.WalkDir(p.directory,func(path string, entry fs.DirEntry, err error) error {
   if err!=nil{return err};if !entry.Type().IsRegular(){return nil};info,err:=entry.Info();if err!=nil{return err};total+=info.Size();return nil
  })
  if err!=nil {row.Errors=[]serving.ResourceError{{Target:p.directory,Field:"observationDirectoryBytes",Reason:err.Error()}}} else {row.ObservationDirectoryBytes=&total}
  p.nextDirectory=at.Truncate(p.directoryInterval).Add(p.directoryInterval)
 }
 return p.log.ResourcesRecord(row)
}
