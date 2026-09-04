import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { AppShell } from '@ark/ui'
import { App } from './app'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <AppShell>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </AppShell>
  </StrictMode>,
)
