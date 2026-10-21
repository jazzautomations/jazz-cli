import sys,json,os
F='todo.json'
t=json.load(open(F)) if os.path.exists(F) else []
c=sys.argv[1]
if c=='add': t.append(' '.join(sys.argv[2:]))
elif c=='done': t.pop(int(sys.argv[2])-1)
elif c=='list':
    for i,x in enumerate(t,1): print(f'{i}. {x}')
json.dump(t,open(F,'w'))
