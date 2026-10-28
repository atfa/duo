'use strict';
const term = new Terminal({cursorBlink:true,fontSize:14,fontFamily:'"DejaVu Sans Mono", "Liberation Mono", monospace',theme:{background:'#0a0e14',foreground:'#dce3ec',cursor:'#5eead4',selectionBackground:'#233442'},scrollback:3000});
const fit = new FitAddon.FitAddon();
term.loadAddon(fit);
term.open(document.getElementById('terminal'));
const status = document.getElementById('status');
const encoder = new TextEncoder();
let socket;
const controls=document.querySelector('.shortcuts');
function state(name,text){if(name!=='connected')controls.hidden=true;status.dataset.state=name;status.textContent=text;document.querySelectorAll('[data-shortcut]').forEach(button=>button.disabled=name!=='connected');}
function resize(){fit.fit();if(socket?.readyState===WebSocket.OPEN)socket.send(JSON.stringify({cols:term.cols,rows:term.rows}));}
const login=document.getElementById('login');
const terminalElement=document.getElementById('terminal');
async function connect(){
 state('connecting','Connecting…');
 let response;
 try{response=await fetch('/api/session',{cache:'no-store'});if(!response.ok)throw new Error();}
 catch{state('disconnected','Server unavailable · reconnect to continue');return;}
 const session=await response.json();
 if(!session.authenticated){
  login.hidden=false;terminalElement.hidden=true;
  state('disconnected','Access code required');document.getElementById('access-code').focus();return;
 }
 login.hidden=true;terminalElement.hidden=false;
 const previous=socket;
 socket=null;
 if(previous)previous.close();
 fit.fit();
 const current = new WebSocket(`${location.protocol==='https:'?'wss:':'ws:'}//${location.host}/ws`);
 socket=current;current.binaryType='arraybuffer';
 state('connecting','Connecting…');
 current.onopen=()=>{if(socket!==current)return;term.reset();resize();state('connected','● Connected');term.focus();};
 current.onmessage=event=>{
  if(socket!==current)return;
  if(typeof event.data==='string'){
   const message=JSON.parse(event.data);
   if(message.type==='session')controls.hidden=!message.duo;
  }else term.write(new Uint8Array(event.data));
 };
 current.onclose=()=>{if(socket===current)state('disconnected','Disconnected · reconnect to continue');};
 current.onerror=()=>{if(socket===current)state('disconnected','Connection failed · check Server in xduo');};
}
term.onData(data=>{if(socket?.readyState===WebSocket.OPEN)socket.send(encoder.encode(data));});
term.onBinary(data=>{if(socket?.readyState===WebSocket.OPEN)socket.send(Uint8Array.from(data,c=>c.charCodeAt(0)));});
const codeInput=document.getElementById('access-code');
codeInput.addEventListener('input',()=>{
 const position=codeInput.selectionStart??codeInput.value.length;
 const before=codeInput.value.slice(0,position).replace(/[^a-z0-9]/gi,'').length;
 const raw=codeInput.value.replace(/[^a-z0-9]/gi,'').toUpperCase().slice(0,8);
 codeInput.value=raw.length>4?raw.slice(0,4)+'-'+raw.slice(4):raw;
 const caret=Math.min(codeInput.value.length,before+(before>4?1:0));
 codeInput.setSelectionRange(caret,caret);
});
document.getElementById('login-form').onsubmit=async event=>{
 event.preventDefault();const error=document.getElementById('login-error');error.textContent='';
 try{const response=await fetch('/api/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({code:document.getElementById('access-code').value})});
 if(!response.ok){error.textContent=await response.text();return;}document.getElementById('access-code').value='';await connect();}
 catch{error.textContent='Server unavailable. Try again.';}
};
new ResizeObserver(resize).observe(document.getElementById('terminal'));
document.getElementById('reconnect').onclick=connect;
document.getElementById('fullscreen').onclick=async()=>{try{if(document.fullscreenElement)await document.exitFullscreen();else await document.documentElement.requestFullscreen();resize();term.focus();}catch{state('disconnected','Fullscreen is unavailable');}};
window.addEventListener('beforeunload',()=>socket?.close());
const shortcuts={models:'\x1bm',austin:'\x01',tony:'\x14',detach:'\x1d',quit:'\x11'};
document.querySelectorAll('[data-shortcut]').forEach(button=>button.onclick=()=>{
 if(socket?.readyState!==WebSocket.OPEN)return;
 socket.send(encoder.encode(shortcuts[button.dataset.shortcut]));
 term.focus();
});
connect();
