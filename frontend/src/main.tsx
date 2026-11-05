import React from 'react';
import ReactDOM from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { BrowserRouter } from 'react-router-dom';
import { App } from './app/App';
import { BrandHomeNavigation } from './app/BrandHomeNavigation';
import { GlobalSearchController } from './app/GlobalSearchController';
import { LegacyRouteRedirects } from './app/LegacyRouteRedirects';
import { PortalAccessGate } from './app/PortalAccessGate';
import { ScrollToTop } from './app/ScrollToTop';
// Standalone frontend builds mirror the official /brand tokens (checked in CI).
import './shared/brand-tokens.css';
import './shared/styles.css';
import './shared/rg45.css';
import './shared/rg61.css';
import './shared/rg82.css';
import './shared/rg80.css';
import './shared/rg80-shell.css';
import './shared/rg80-dashboard.css';
import './shared/rg80-dashboard-tables.css';
import './shared/rg80-feature-reference.css';
import './shared/rg80-auth.css';
import './shared/rg80-locale.css';
import './shared/rg-spacing-audit.css';
import './shared/rg101-security.css';
import './shared/rg-shell-cleanup.css';
import './shared/rg-status-glass.css';
import './shared/rg130-mobile-safe-area.css';
import './shared/rg131-portal-mobile.css';
import './shared/rg114-ui-acceptance.css';
import './shared/rg-dashboard-live.css';
import './shared/rg-brand.css';

const queryClient = new QueryClient();
const rootElement = document.getElementById('root');

if (rootElement) {
  ReactDOM.createRoot(rootElement).render(
    <React.StrictMode>
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <ScrollToTop />
          <LegacyRouteRedirects />
          <PortalAccessGate>
            <App />
            <BrandHomeNavigation />
            <GlobalSearchController />
          </PortalAccessGate>
        </BrowserRouter>
      </QueryClientProvider>
    </React.StrictMode>,
  );
}
