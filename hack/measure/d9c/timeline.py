# timeline.py: per-20s batch starts, batchID span and rows for one workload window (same fetch as split.py)
import sys,json,collections
from pathlib import Path
exec(Path(__file__).with_name("split.py").read_text().split("def P(s)")[0])
import datetime as dt
def P(s):
    b,_,f=s.rstrip('Z').partition('.'); return dt.datetime.fromisoformat(b+'.'+f[:6].ljust(6,'0')).timestamp()
c=collections.Counter(); ids=collections.defaultdict(set); oc=collections.Counter()
for r in rows:
    oc[r.get('outcome')]+=1
    if r.get('runningNanos') is None: continue
    t=P(r['receivedAt'])+r['runningNanos']/1e9; k=int(t//20)*20; ids[k].add(r['batchID'])
for k in sorted(ids): print(dt.datetime.utcfromtimestamp(k).strftime('%T'), len(ids[k]), min(ids[k]), max(ids[k]), max(ids[k])-min(ids[k])+1)
print(oc)
