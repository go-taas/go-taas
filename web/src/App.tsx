import { useEffect, useState } from 'react';
import { Router, Routes, Route, navigate } from './router';
import { OrgProvider, OrgSwitcher } from './org';
import { api, getSessionToken, setSessionToken } from './api';
import LoginPage from './pages/LoginPage';
import OrganizationsPage from './pages/OrganizationsPage';
import ProjectsPage from './pages/ProjectsPage';
import SSOProvidersPage from './pages/SSOProvidersPage';
import IdentityBindingsPage from './pages/IdentityBindingsPage';
import ApiKeysPage from './pages/ApiKeysPage';
import ModelsPage from './pages/ModelsPage';
import ModelDetailPage from './pages/ModelDetailPage';
import InferenceServicesPage from './pages/InferenceServicesPage';
import ServiceDetailPage from './pages/ServiceDetailPage';
import ImagesPage from './pages/ImagesPage';
import ImageDetailPage from './pages/ImageDetailPage';
import UsagePage from './pages/UsagePage';
import PricingPage from './pages/PricingPage';
import BillsPage from './pages/BillsPage';
import AccountsPage from './pages/AccountsPage';
import NotFoundPage from './pages/NotFoundPage';

export default function App() {
  return (
    <OrgProvider>
      <Router>
        <Layout>
          <Routes>
            <Route path="/" element={<NavigateToAdmin />} />
            <Route path="/admin/login" element={<LoginPage />} />
            <Route path="/admin" element={<NavigateToModels />} />
            <Route path="/admin/organizations" element={<OrganizationsPage />} />
            <Route path="/admin/projects" element={<ProjectsPage />} />
            <Route path="/admin/sso" element={<SSOProvidersPage />} />
            <Route path="/admin/identity-bindings" element={<IdentityBindingsPage />} />
            <Route path="/admin/api-keys" element={<ApiKeysPage />} />
            <Route path="/admin/models" element={<ModelsPage />} />
            <Route path="/admin/models/:id" element={<ModelDetailPage />} />
            <Route path="/admin/images" element={<ImagesPage />} />
            <Route path="/admin/images/:id" element={<ImageDetailPage />} />
            <Route path="/admin/inference-services" element={<InferenceServicesPage />} />
            <Route path="/admin/inference-services/:id" element={<ServiceDetailPage />} />
            <Route path="/admin/usage" element={<UsagePage />} />
            <Route path="/admin/pricing" element={<PricingPage />} />
            <Route path="/admin/billing" element={<BillsPage />} />
            <Route path="/admin/billing/accounts" element={<AccountsPage />} />
            <Route path="*" element={<NotFoundPage />} />
          </Routes>
        </Layout>
      </Router>
    </OrgProvider>
  );
}

// The entry route redirects into the admin console.
// isActive reports whether a nav item covers the current path: an
// exact match or a path-segment prefix (so /admin/billing/accounts
// highlights Accounts but not Bills).
function isActive(path: string, current: string): boolean {
  return current === path || current.startsWith(`${path}/`);
}

function NavigateToAdmin() {
  useEffect(() => {
    navigate('/admin');
  }, []);
  return null;
}

// /admin redirects to the models catalog (the console's home).
function NavigateToModels() {
  useEffect(() => {
    navigate('/admin/models');
  }, []);
  return null;
}

function Layout({ children }: { children: React.ReactNode }) {
  return (
    <div className="app">
      <Sidebar />
      <main className="main">{children}</main>
    </div>
  );
}

const NAV_ITEMS = [
  { path: '/admin/organizations', label: 'Organizations' },
  { path: '/admin/projects', label: 'Projects' },
  { path: '/admin/sso', label: 'SSO Providers' },
  { path: '/admin/identity-bindings', label: 'Identity Bindings' },
  { path: '/admin/models', label: 'Models' },
  { path: '/admin/inference-services', label: 'Inference Services' },
  { path: '/admin/images', label: 'Images' },
  { path: '/admin/api-keys', label: 'API Keys' },
  { path: '/admin/usage', label: 'Usage' },
  { path: '/admin/pricing', label: 'Pricing' },
  { path: '/admin/billing', label: 'Bills' },
  { path: '/admin/billing/accounts', label: 'Accounts' },
];

function Sidebar() {
  const [path, setPath] = useState(window.location.pathname);
  useEffect(() => {
    return Router.subscribe(() => setPath(window.location.pathname));
  }, []);
  const logout = async () => {
    try {
      await api.post('/api/v1/auth/logout', '', {});
    } catch {
      // Ignore logout errors; clear the session regardless.
    }
    setSessionToken('');
    navigate('/admin/login');
  };
  return (
    <aside className="sidebar" data-testid="sidebar">
      <div className="brand">go-taas</div>
      <nav>
        {NAV_ITEMS.map((item) => (
          <a
            key={item.path}
            href={item.path}
            className={isActive(item.path, path) ? 'nav-item active' : 'nav-item'}
            data-testid={`nav-${item.label.toLowerCase().replace(/\s+/g, '-')}`}
            onClick={(e) => {
              e.preventDefault();
              navigate(item.path);
            }}
          >
            {item.label}
          </a>
        ))}
      </nav>
      <OrgSwitcher />
      {getSessionToken() && (
        <button
          className="link"
          data-testid="user-menu-logout"
          onClick={() => void logout()}
        >
          Sign out
        </button>
      )}
    </aside>
  );
}
