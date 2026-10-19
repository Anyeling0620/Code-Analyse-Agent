import React from 'react';
import ReactDOM from 'react-dom/client';
import App from './App';
import './styles.css';
import './theme/appearance.css';
import './theme/designs/nocturne.css';
import './theme/designs/editorial.css';
import './theme/designs/schematic.css';

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
