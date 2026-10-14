package opencode

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Execute the generated V1 JavaScript with a fixture host. This qualifies the
// shim logic, not a live OpenCode release or the incompatible V2 plugin API.
func TestGeneratedPluginSessionIsolationAndContinuation(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable: generated plugin execution not qualified")
	}
	dir := t.TempDir()
	plugin := filepath.Join(dir, "plugin.mjs")
	if err := os.WriteFile(plugin, []byte(buildPluginJS("/fixture/agent-brain")), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `import { AgentBrainPlugin } from './plugin.mjs';
import assert from 'node:assert/strict';
const calls=[], prompts=[];
let plugin;
const shell=(strings,...args)=>{
 const payload=JSON.parse(args[0]),event=args[2];calls.push({event,payload});
 return {nothrow(){return this},quiet:async()=>({stdout:JSON.stringify({additionalContext:event==='stop'?'capture '+payload.session_id:'context '+payload.session_id})})};
};
plugin=await AgentBrainPlugin({directory:'/project', $:shell,client:{session:{prompt:async p=>{prompts.push(p);await plugin.event({event:{type:'session.idle',properties:{sessionID:p.path.id}}});}}}});
await plugin.event({event:{type:'session.created',properties:{info:{id:'foreign',directory:'/other'}}}});
await plugin.event({event:{type:'session.idle',properties:{sessionID:'foreign'}}});
assert.equal(calls.length,0);
for(const sid of ['a','b']) await plugin.event({event:{type:'session.created',properties:{info:{id:sid,directory:'/project'}}}});
const a={system:[]};await plugin['experimental.chat.system.transform']({sessionID:'a'},a);
assert(a.system.join('').includes('context a'));assert(!a.system.join('').includes('context b'));
const missing={system:[]};await plugin['experimental.chat.system.transform']({},missing);assert.equal(missing.system.length,0);
await plugin['chat.message']({sessionID:'a'},{parts:[{type:'text',text:'normal user prompt'}]});
await plugin['tool.execute.after']({sessionID:'b',tool:'read'});
await plugin.event({event:{type:'session.idle',properties:{sessionID:'a'}}});assert.equal(prompts.length,1);assert.equal(prompts[0].path.id,'a');
const n=calls.length;await plugin['chat.message']({sessionID:'a'},{parts:prompts[0].body.parts});assert.equal(calls.length,n);
const info={id:'m1',sessionID:'b',role:'assistant',finish:'stop',tokens:{input:10,output:5}};
for(let i=0;i<2;i++)await plugin.event({event:{type:'message.updated',properties:{info}}});
assert.equal(calls.filter(c=>c.event==='model-usage').length,1);
assert(calls.every(c=>c.payload.cwd==='/project' && ['a','b'].includes(c.payload.session_id)));
assert(!JSON.stringify(calls).includes('normal user prompt'));
console.log('V1 fixture session isolation, context, bounded reentry, usage dedupe passed');`
	path := filepath.Join(dir, "test.mjs")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, path).CombinedOutput()
	if err != nil {
		t.Fatalf("generated plugin: %s %v", out, err)
	}
	t.Log(string(out))
}
