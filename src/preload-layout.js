// The DC Layout Builder window: past 512 racks (2.0.20) it asks the app's Layout service for the links.
'use strict';
const { contextBridge, ipcRenderer } = require('electron');
contextBridge.exposeInMainWorld('aidcLayout', {
  q: (op, args) => ipcRenderer.invoke('layout-q', op, args || {}),
  onProgress: (fn) => { ipcRenderer.on('layout-progress', (e, t) => { try { fn(String(t)); } catch (err) {} }); }
});
