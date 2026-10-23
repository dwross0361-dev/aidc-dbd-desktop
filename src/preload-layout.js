// The DC Layout Builder window: it asks the app for the model, and the app asks the builder window.
'use strict';
const { contextBridge, ipcRenderer } = require('electron');
contextBridge.exposeInMainWorld('aidcLayout', {
  requestModel: () => ipcRenderer.invoke('layout-model')
});
