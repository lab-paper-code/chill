package main

import (
 "os"
 "path/filepath"
 "testing"
 "time"
 "github.com/lab-paper-code/chill/internal/serving"
)

// SCH-AC-53: identities and actual mount records, not a guessed runtime layout.
func TestResourceCgroupVersionsAndMissingPeak(t *testing.T) {
 target:=serving.ResourceTarget{PodUID:"aa-bb",ContainerID:"containerd://deadbeef"}
 for _,version:=range []int{1,2} {
  t.Run(string(rune('0'+version)),func(t *testing.T){
   root:=t.TempDir()
   group:="/arbitrary/podaa-bb/deadbeef"
   if version==2 {
    writeHostFile(t,root,"proc/77/cgroup","0::"+group+"\n")
    writeHostFile(t,root,"proc/77/mountinfo","30 20 0:1 / /sys/custom rw - cgroup2 cgroup rw\n")
    writeHostFile(t,root,"sys/custom"+group+"/cpu.stat","usage_usec 7\nthrottled_usec 3\nnr_periods 5\nnr_throttled 2\n")
    writeHostFile(t,root,"sys/custom"+group+"/memory.current","13\n")
   }else {
    writeHostFile(t,root,"proc/77/cgroup","2:cpu,cpuacct:"+group+"\n3:memory:"+group+"\n")
    writeHostFile(t,root,"proc/77/mountinfo","30 20 0:1 / /sys/unusual/cpu rw - cgroup cgroup rw,cpu,cpuacct\n31 20 0:2 / /sys/unusual/memory rw - cgroup cgroup rw,memory\n")
    writeHostFile(t,root,"sys/unusual/cpu"+group+"/cpuacct.usage","7000\n")
    writeHostFile(t,root,"sys/unusual/cpu"+group+"/cpu.stat","throttled_time 3000\nnr_periods 5\nnr_throttled 2\n")
    writeHostFile(t,root,"sys/unusual/memory"+group+"/memory.usage_in_bytes","13\n")
   }
   writeHostFile(t,root,"sys/class/thermal/thermal_zone4/type","chip\n")
   writeHostFile(t,root,"sys/class/thermal/thermal_zone4/temp","42000\n")
   at:=time.Date(2026,10,30,1,2,3,0,time.UTC)
   sample:=SampleResources(root,[]serving.ResourceTarget{target},at)
   row:=sample.Containers[0]
   if row.CgroupVersion!=version || row.CPUUsageNanos==nil || *row.CPUUsageNanos!=7000 || row.ThrottledNanos==nil || *row.ThrottledNanos!=3000 || row.MemoryCurrentBytes==nil || *row.MemoryCurrentBytes!=13 {t.Fatalf("sample=%+v container=%+v",sample,row)}
   if row.MemoryPeakBytes!=nil || len(row.Errors)==0 {t.Fatalf("missing peak was fabricated: %+v",row)}
   if sample.SampledAt!=at || len(sample.Thermal)!=1 || sample.Thermal[0].MilliCelsius!=42000 {t.Fatalf("timestamp/thermal=%+v",sample)}
  })
 }
 if matchesResourceIdentity("0::/podaa-bb/deadbeef1\n",target) {t.Fatal("partial container identity accepted")}
 if !matchesResourceIdentity("0::/kubepods-burstable-podaa_bb.slice/cri-containerd-deadbeef.scope\n",target) {t.Fatal("systemd identity not discovered")}
}

func writeHostFile(t *testing.T, root, name, value string) {
 t.Helper()
 path:=filepath.Join(root,name)
 if err:=os.MkdirAll(filepath.Dir(path),0700);err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(path,[]byte(value),0600);err!=nil{t.Fatal(err)}
}
