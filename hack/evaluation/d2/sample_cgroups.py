#!/usr/bin/env python3
"""CPO-executed finite read-only host sampler; no Kubernetes/API/attach calls.
--hosts is an explicitly bound list {node,local:true} or {node,ssh:[verified ssh argv]}.
Remote argv must terminate in python3 -u -; it receives this same numeric sampler.
"""
import argparse
import datetime as dt
import json
from pathlib import Path
import signal
import subprocess
import sys
import time

REMOTE = r'''
import datetime as dt, glob, json, pathlib, subprocess, sys, time
node, duration, phase = sys.argv[1], float(sys.argv[2]), sys.argv[3]
end=time.monotonic()+duration
containers=[]; next_resolve=0; next_thermal=0
initial=None

def emit(row):
    row.update(at=dt.datetime.now(dt.timezone.utc).isoformat(),node=node,phase=phase)
    print(json.dumps(row,separators=(',',':')),flush=True)

def run(argv):
    p=subprocess.run(argv,capture_output=True,text=True,timeout=10)
    if p.returncode: raise RuntimeError(' '.join(argv[:3])+': '+p.stderr.strip()[:250])
    return json.loads(p.stdout)

def text(path):
    try: return pathlib.Path(path).read_text().strip()
    except OSError: return None

def integer(path):
    try: return int(text(path))
    except (TypeError,ValueError): return None

def read_cgroup_counters(paths):
    # The exact three affected hosts expose cgroup2 at /sys/fs/cgroup, root /.
    # Preserve the already working cpuacct v1 path on hybrid hosts.
    unified='cpuacct' not in paths and '' in paths
    relative=paths.get('') if unified else paths.get('cpuacct')
    cpu='/sys/fs/cgroup'+relative if unified else '/sys/fs/cgroup/cpu,cpuacct'+paths.get('cpuacct','!missing')
    memory_path=cpu+'/memory.current' if unified else '/sys/fs/cgroup/memory'+paths.get('memory','!missing')+'/memory.usage_in_bytes'
    raw=text(cpu+'/cpu.stat');stat={}
    if raw:
        for line in raw.splitlines():
            key,value=line.split();stat[key]=int(value)
    if unified:
        usage=stat.get('usage_usec');throttled=stat.get('throttled_usec')
        usage=None if usage is None else usage*1000
        throttled=None if throttled is None else throttled*1000
    else:
        usage=integer(cpu+'/cpuacct.usage');throttled=stat.get('throttled_time')
    return dict(cgroup=relative,cgroupVersion=2 if unified else 1,cpuUsageNanos=usage,nrThrottled=stat.get('nr_throttled'),throttledNanos=throttled,memoryBytes=integer(memory_path),counterSources=dict(cpuStat=cpu+'/cpu.stat',memory=memory_path),memoryMeaning='Current cgroup charged bytes, not working set')

while time.monotonic()<end:
    began=time.monotonic()
    if began>=next_resolve:
        try:
            found=[]
            candidates=initial if initial is not None else run(['sudo','-n','crictl','ps','-o','json']).get('containers',[])
            initial=None
            for c in candidates:
                labels=c.get('labels',{}); ns=labels.get('io.kubernetes.pod.namespace'); name=c.get('metadata',{}).get('name')
                pod=labels.get('io.kubernetes.pod.name','')
                role=None
                if ns=='chill-system' and name=='serving-entrypoint': role='server'
                elif ns=='kubeedge' and name=='edgemesh-agent': role='edgemesh'
                elif ns=='chill-system' and name=='firstfloor': role='native'
                elif ns=='chill-system' and name=='load' and pod.startswith('d2-before-'): role='client'
                if role is None: continue
                cid=c['id']; error=None
                try:
                    inspected=run(['sudo','-n','crictl','inspect',cid]) if cid else {}
                    pid=inspected.get('info',{}).get('pid')
                except RuntimeError as exc:
                    pid=None; error=str(exc)
                group=text('/proc/'+str(pid)+'/cgroup') if pid else None
                paths={}
                for line in (group or '').splitlines():
                    _, controllers, rel=line.split(':',2)
                    for controller in controllers.split(','): paths[controller]=rel
                found.append(dict(role=role,containerID=cid,pod=pod,podUID=labels.get('io.kubernetes.pod.uid'),pid=pid,paths=paths,procCgroup=group,pidAbsent=not bool(pid and group),pidError=error,restartCount=c.get('metadata',{}).get('attempt')))
            containers=found
            emit(dict(kind='inventory',containers=[{k:v for k,v in c.items() if k not in ('paths','procCgroup')} for c in containers]))
        except Exception as e: emit(dict(kind='collector-error',operation='resolve',error=str(e)))
        next_resolve=began+15
    for c in containers:
        row={k:v for k,v in c.items() if k not in ('pid','paths','procCgroup')}
        absent=c['pidAbsent'] or text('/proc/'+str(c['pid'])+'/cgroup') != c['procCgroup']
        row.update(kind='cgroup',pidAbsent=absent)
        if absent:
            row.update(cpuUsageNanos=None,nrThrottled=None,throttledNanos=None,memoryBytes=None)
        else:
            row.update(read_cgroup_counters(c['paths']))
        if absent or row.get('cpuUsageNanos') is None: next_resolve=0
        emit(row)
    if began>=next_thermal:
        zones=[]
        for path in sorted(glob.glob('/sys/class/thermal/thermal_zone*')):
            zones.append(dict(path=path,type=text(path+'/type'),millidegreesC=integer(path+'/temp')))
        emit(dict(kind='thermal',zones=zones,activeThrottle=None,latchedThrottle=None,note='Thermal/throttle unknown when no native sensor is exposed; no manufactured bins.'))
        next_thermal=began+5
    time.sleep(max(0,1-(time.monotonic()-began)))
'''


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--hosts',required=True,type=Path)
    p.add_argument('--duration',required=True,type=float)
    p.add_argument('--phase',required=True)
    p.add_argument('--out',required=True,type=Path)
    a=p.parse_args()
    if not a.hosts.is_absolute() or not a.out.is_absolute() or not 0<a.duration<=4200:
        p.error('Absolute paths and finite0<duration<=4200 are required')
    hosts=json.loads(a.hosts.read_text())
    if not isinstance(hosts,list) or not hosts: p.error('Bound nonempty host list required')
    a.out.mkdir(parents=True,exist_ok=True)
    processes=[]
    def stop(_signum, _frame):
        for _node, child, _out, _err, _argv in processes:
            if child.poll() is None:
                child.terminate()
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    for h in hosts:
        node=h['node']
        if h.get('local'):
            argv=[sys.executable,'-u','-',node,str(a.duration),a.phase]
        else:
            ssh=h.get('ssh')
            if not isinstance(ssh,list) or not ssh or ssh[-3:]!=['python3','-u','-']:
                p.error('Each remote ssh argv must be verified and end python3 -u -')
            argv=ssh+[node,str(a.duration),a.phase]
        out=(a.out/(node+'-cgroup.jsonl')).open('w')
        err=(a.out/(node+'-collector.stderr')).open('w')
        proc=subprocess.Popen(argv,stdin=subprocess.PIPE,stdout=out,stderr=err,text=True)
        proc.stdin.write(REMOTE); proc.stdin.close()
        processes.append((node,proc,out,err,argv))
    status=[]
    for node,proc,out,err,argv in processes:
        try: rc=proc.wait(timeout=a.duration+60)
        except subprocess.TimeoutExpired:
            proc.terminate()
            try: rc=proc.wait(timeout=10)
            except subprocess.TimeoutExpired: proc.kill(); rc=proc.wait()
        out.close();err.close();status.append(dict(node=node,argv=argv,exitCode=rc))
    (a.out/'collector-status.json').write_text(json.dumps(status,indent=2)+'\n')
    return 0 if all(x['exitCode']==0 for x in status) else 1

if __name__=='__main__':
    sys.exit(main())
