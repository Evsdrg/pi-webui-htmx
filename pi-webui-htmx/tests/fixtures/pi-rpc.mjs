#!/usr/bin/env node
// 浏览器验收专用 RPC 进程；只读写显式传入的隔离目录，不调用模型。
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import readline from 'node:readline';
const args = process.argv.slice(2);
const arg = (key) => { const i = args.indexOf(key); return i < 0 ? '' : args[i + 1] ?? ''; };
const now = () => new Date().toISOString();
const fence = String.fromCharCode(96).repeat(3);

if (arg('--seed')) {
  const root = path.resolve(arg('--seed')); const cwd = path.resolve(arg('--workspace'));
  fs.mkdirSync(path.join(root,'sessions','fixture'),{recursive:true}); fs.mkdirSync(path.join(root,'agent'),{recursive:true}); fs.mkdirSync(cwd,{recursive:true});
  fs.writeFileSync(path.join(root,'agent','models.json'),JSON.stringify({providers:{fixture:{api:'openai-completions',baseUrl:'http://127.0.0.1:1',models:[{id:'demo-fast',name:'演示 · 快速模型',reasoning:true,contextWindow:128000,maxTokens:4096},{id:'demo-review',name:'演示 · 审阅模型'}]}}},null,2));
  fs.writeFileSync(path.join(root,'agent','settings.json'),JSON.stringify({packages:[]}));
  fs.writeFileSync(path.join(cwd,'README.md'),'# 示例工作区\n\n这是浏览器验收用的隔离目录。\n');
  const rows = [{type:'session',version:3,id:'history-demo',timestamp:now(),cwd}]; let parent = null;
  const append = (entry) => { const id = entry.id; rows.push({...entry,parentId:parent,timestamp:now()}); parent=id; };
  append({type:'session_info',id:'title',name:'桥与前端 · 设计评审'});
  for(let i=1;i<=36;i++){
    append({type:'message',id:`u${i}`,message:{role:'user',content:`第 ${i} 轮：请检查工作区结构，给出明确的实现建议。`}});
    append({type:'message',id:`p${i}`,message:{role:'assistant',content:[{type:'text',text:'先核对文件和现有接口。'}]}});
    append({type:'message',id:`t${i}`,message:{role:'toolResult',toolCallId:`call-${i}`,toolName:'read',content:[{type:'text',text:'README.md\nsrc/\ntests/'}]}});
    append({type:'message',id:`a${i}`,message:{role:'assistant',content:[{type:'text',text:`### 第 ${i} 轮检查结果\n\n前端与桥保持清晰的职责边界：\n\n- **页面**负责交互和局部渲染。\n- **桥**负责进程与持久历史。\n- 每个错误都应有可见反馈。\n\n${fence}typescript\nconst ready = true;\nconsole.log(ready);\n${fence}\n\n| 检查 | 状态 |\n| --- | --- |\n| 协议 | 通过 |\n| 资源 | 有界 |`+(i===36?'\n\n```mermaid\ngraph LR\n  Browser-->Bridge\n  Bridge-->Pi\n```\n\n$$x^2 + y^2 = z^2$$':'')}]}});
  }
  fs.writeFileSync(path.join(root,'sessions','fixture','history-demo.jsonl'),rows.map(v=>JSON.stringify(v)).join('\n')+'\n');
  console.log(JSON.stringify({stateDir:root,workspace:cwd,sessions:1,turns:36})); process.exit(0);
}

const sessionDir = arg('--session-dir');
let file = arg('--session');
let entries = file ? fs.readFileSync(file,'utf8').trim().split('\n').map(v=>JSON.parse(v)) : [];
let id = entries[0]?.id ?? crypto.randomUUID();
let parent = entries.at(-1)?.id ?? null;
let name = entries.filter(v=>v.type==='session_info').at(-1)?.name ?? '浏览器测试会话';
let active = false; let cancel = false; let thinking = 'off';
let steeringMode = 'all'; let followUpMode = 'all'; let autoCompaction = false; let autoRetry = false; let model = {id:'demo-fast',name:'演示 · 快速模型',provider:'fixture'};
const dialogs = new Map();
const emit = value => process.stdout.write(JSON.stringify(value)+'\n');
const reply = (cmd,data={}) => emit({type:'response',command:cmd.type,id:cmd.id,success:true,data});
const replyErr = (cmd,message) => emit({type:'response',command:cmd.type,id:cmd.id,success:false,error:message});
function append(entry){
 if(!file){const dir=path.join(sessionDir,'fixture');fs.mkdirSync(dir,{recursive:true});file=path.join(dir,`${id}.jsonl`);const header={type:'session',version:3,id,timestamp:now(),cwd:process.cwd()};fs.writeFileSync(file,JSON.stringify(header)+'\n');entries=[header];}
 const row={...entry,id:crypto.randomUUID(),parentId:parent,timestamp:now()};parent=row.id;entries.push(row);fs.appendFileSync(file,JSON.stringify(row)+'\n');
}
const pause = ms => new Promise(resolve=>setTimeout(resolve,ms));
async function prompt(cmd){
 if(active){reply(cmd);return;} active=true;cancel=false;reply(cmd);emit({type:'agent_start'});
 append({type:'message',message:{role:'user',content:cmd.message}});
 emit({type:'extension_ui_request',id:crypto.randomUUID(),method:'setStatus',statusKey:'fixture',statusText:'通用扩展 · 正在处理'});
 emit({type:'extension_ui_request',id:crypto.randomUUID(),method:'setWidget',widgetKey:'fixture-task',widgetLines:['核对请求','流式输出','保存历史'],widgetPlacement:'aboveEditor'});
 if(cmd.message.includes('/dialog')){
  const dialogId=crypto.randomUUID();emit({type:'extension_ui_request',id:dialogId,method:'confirm',title:'继续检查工作区？',message:'这是通用扩展对话，用于验证回执。'});
  await new Promise(resolve=>dialogs.set(dialogId,resolve));
 }
 const answer=cmd.message.includes('慢速')?'正在逐步检查工作区，连接断开后任务仍会继续。'.repeat(25):`已收到：${cmd.message}\n\n**桥接成功。** 消息经 WebSocket → Go 桥 → stdio RPC 返回。\n\n${fence}ts\nconst connected = true;\n${fence}`;
 emit({type:'message_start',message:{role:'assistant',content:[]}});let output='';
 for(let i=0;i<answer.length&&!cancel;i+=8){const delta=answer.slice(i,i+8);output+=delta;emit({type:'message_update',assistantMessageEvent:{type:'text_delta',contentIndex:0,delta}});await pause(cmd.message.includes('慢速')?80:12);}
 const message={role:'assistant',content:[{type:'text',text:output+(cancel?'\n\n[已中止]':'')}],stopReason:cancel?'aborted':'stop'};append({type:'message',message});emit({type:'message_end',message});emit({type:'agent_end',messages:[message]});active=false;emit({type:'agent_settled'});
 emit({type:'extension_ui_request',id:crypto.randomUUID(),method:'setStatus',statusKey:'fixture',statusText:'通用扩展 · 空闲'});emit({type:'extension_ui_request',id:crypto.randomUUID(),method:'notify',message:'隔离任务已完成',notifyType:'info'});
}
const lines=readline.createInterface({input:process.stdin});
lines.on('line',line=>{let cmd;try{cmd=JSON.parse(line);}catch{return;}
 switch(cmd.type){
 case 'get_state':reply(cmd,{sessionId:id,sessionName:name,model,thinkingLevel:thinking,isStreaming:active,isCompacting:false,pendingMessageCount:0,messageCount:entries.length,steeringMode,followUpMode,autoCompactionEnabled:autoCompaction});break;
 case 'prompt':void prompt(cmd);break;
 case 'abort':cancel=true;for(const resolve of dialogs.values())resolve();dialogs.clear();reply(cmd,{steering:[],followUp:[]});break;
 case 'extension_ui_response':dialogs.get(cmd.id)?.();dialogs.delete(cmd.id);break;
 case 'get_available_models':reply(cmd,{models:[model,{id:'demo-review',name:'演示 · 审阅模型',provider:'fixture'}]});break;
 case 'set_model':model={id:cmd.modelId,provider:cmd.provider,name:cmd.modelId};reply(cmd,model);break;
 case 'get_available_thinking_levels':reply(cmd,{levels:['off','low','high']});break;
 case 'set_thinking_level':thinking=cmd.level;reply(cmd);break;
 case 'set_steering_mode':steeringMode=cmd.mode;reply(cmd);break;
 case 'set_follow_up_mode':followUpMode=cmd.mode;reply(cmd);break;
 case 'set_auto_compaction':autoCompaction=cmd.enabled;reply(cmd,{enabled:autoCompaction});break;
 case 'set_auto_retry':autoRetry=cmd.enabled;reply(cmd,{enabled:autoRetry});break;
 case 'abort_retry':autoRetry=false;reply(cmd,{aborted:true});break;
 // 桥传 outputPath 并要求返回值与之一致；夹具必须按它给的位置写，
 // 否则桥的「路径与请求不一致」校验会正确拒绝。
 case 'export_html':{const file=cmd.outputPath;if(!file){replyErr(cmd,'缺少 outputPath');break;}fs.mkdirSync(path.dirname(file),{recursive:true});fs.writeFileSync(file,'<html><body>导出自测试夹具</body></html>');reply(cmd,{path:file});break;}
 case 'get_commands':reply(cmd,{commands:[{name:'dialog',description:'测试通用扩展对话',source:'extension'},{name:'review',description:'检查当前工作区',source:'extension'}]});break;
 case 'get_session_stats':reply(cmd,{sessionId:id,totalMessages:entries.length,cost:0});break;
 case 'set_session_name':name=cmd.name;append({type:'session_info',name});reply(cmd);break;
 case 'compact':reply(cmd,{summary:'已完成隔离压缩',firstKeptEntryId:parent,tokensBefore:100});break;
 // fork：按 entryId 截断历史，写成一份新会话文件，然后把当前会话切过去。
 // 真实 Pi 会改 sessionId 并保留 parentSession 指针；夹具至少要做到
 // 「返回的 ID 与原来不同」，否则 UI 的切换路径根本验不到。
 case 'fork':{
  const cut=entries.findIndex(v=>v.id===cmd.entryId);
  if(cut<0){replyErr(cmd,'找不到条目 '+cmd.entryId);break;}
  // 必须剔掉原会话头：entries[0] 就是它，复制过来会让新文件出现两个
  // type:"session" 行，桥的历史读取器只认第一行，会直接报格式错误。
  const keep=entries.slice(0,cut+1).filter(v=>v.type!=='session');
  const newId='fork-'+Math.random().toString(16).slice(2,10);
  const header={type:'session',version:3,id:newId,parentSession:file,cwd:process.cwd(),timestamp:new Date().toISOString()};
  const rows=[header,...keep.map(v=>({...v,parentSession:file}))];
  const target=path.join(path.dirname(file),newId+'.jsonl');
  fs.mkdirSync(path.dirname(target),{recursive:true});
  fs.writeFileSync(target,rows.map(v=>JSON.stringify(v)).join('\n')+'\n');
  file=target;id=newId;entries=rows;
  reply(cmd,{sessionId:newId});
  break;
 }
 case 'get_last_assistant_text':reply(cmd,{text:'测试完成'});break;
 // 会话树：按 entries 的 parentId 现搭一棵，叶子取最后一条。
 // 树结构与磁盘一致，浏览器看到的分叉点是真的。
 case 'get_tree':{
  const nodes=new Map();
  for(const row of entries){ if(row.type==='session')continue; nodes.set(row.id,{entry:row,children:[]}); }
  const roots=[];
  for(const row of entries){
   if(row.type==='session')continue;
   const node=nodes.get(row.id);
   if(row.parentId&&nodes.has(row.parentId))nodes.get(row.parentId).children.push(node);
   else roots.push(node);
  }
  const leaf=entries.filter(v=>v.type!=='session').at(-1)?.id??null;
  reply(cmd,{tree:roots,leafId:leaf});
  break;
 }
 case 'get_fork_messages':{
  const messages=entries.filter(v=>v.type==='message'&&v.message?.role==='user').map(v=>({entryId:v.id,text:v.message.content}));
  reply(cmd,{messages});
  break;
 }
 default:reply(cmd);
 }
});
lines.on('close',()=>process.exit(0));
