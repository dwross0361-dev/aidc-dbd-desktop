// The prompt window: its two buttons answer the builder's window.prompt.
'use strict';
const { contextBridge, ipcRenderer } = require('electron');
contextBridge.exposeInMainWorld('aidcPrompt', { answer: (v) => ipcRenderer.send('prompt-answer', v) });
