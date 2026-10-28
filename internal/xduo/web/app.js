'use strict';
const term = new Terminal({cursorBlink:true,fontSize:14,fontFamily:'"DejaVu Sans Mono", "Liberation Mono", monospace',theme:{background:'#0a0e14',foreground:'#dce3ec',cursor:'#5eead4',selectionBackground:'#233442'},scrollback:3000});
const fit = new FitAddon.FitAddon();
term.loadAddon(fit);
term.open(document.getElementById('terminal'));
const status = document.getElementById('status');
const encoder = new TextEncoder();
const token = location.hash.slice(1);
let socket;
function state(name,text){status.dataset.state=name;status.textContent=text;}
function resize(){fit.fit();if(socket?.readyState===WebSocket.OPEN)socket.send(JSON.stringify({cols:term.cols,rows:term.rows}));}
function connect(){
 if(!token){state('disconnected','Open the access link shown in xduo');return;}
 const previous=socket;
 socket=null;
 if(previous)previous.close();
 fit.fit();
 const current = new WebSocket(`ws://${location.host}/ws?token=${encodeURIComponent(token)}`);
 socket=current;current.binaryType='arraybuffer';
 state('connecting','Connecting…');
 current.onopen=()=>{if(socket!==current)return;term.reset();resize();state('connected','● Connected');term.focus();};
 current.onmessage=event=>{if(socket===current)term.write(new Uint8Array(event.data));};
 current.onclose=()=>{if(socket===current)state('disconnected','Disconnected · reconnect to continue');};
 current.onerror=()=>{if(socket===current)state('disconnected','Connection failed · check Server in xduo');};
}
term.onData(data=>{if(socket?.readyState===WebSocket.OPEN)socket.send(encoder.encode(data));});
term.onBinary(data=>{if(socket?.readyState===WebSocket.OPEN)socket.send(Uint8Array.from(data,c=>c.charCodeAt(0)));});
new ResizeObserver(resize).observe(document.getElementById('terminal'));
document.getElementById('reconnect').onclick=connect;
document.getElementById('fullscreen').onclick=async()=>{try{if(document.fullscreenElement)await document.exitFullscreen();else await document.documentElement.requestFullscreen();resize();term.focus();}catch{state('disconnected','Fullscreen is unavailable');}};
window.addEventListener('beforeunload',()=>socket?.close());
connect();
