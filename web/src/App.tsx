// Console surface separation (feature-17): two route trees and two shells
// in one bundle. The surface router evaluates the moved-admin-route
// redirects first (AD7), then picks the user or admin surface by path.

import { useEffect } from 'react';
import { Router, Routes, Route, replace, useRoute } from './router';
import { OrgProvider } from './org';
import { SurfaceProvider } from './surface';
import { isAdminPath, MOVED_ADMIN_ROUTES, realmHome } from './surface-routes';
import { UserShell } from './shells/UserShell';
import { AdminShell } from './shells/AdminShell';
import UserLoginPage from './pages/user/UserLoginPage';
import UserUsagePage from './pages/user/UsagePage';
import UserApiKeysPage from './pages/user/ApiKeysPage';
import UserRequestLogsPage from './pages/user/RequestLogsPage';
import UserPlaygroundPage from './pages/user/PlaygroundPage';
import UserBillsPage from './pages/user/BillsPage';
import AdminLoginPage from './pages/AdminLoginPage';
import OrganizationsPage from './pages/OrganizationsPage';
import ProjectsPage from './pages/ProjectsPage';
import MembersPage from './pages/MembersPage';
import InvitationsPage from './pages/InvitationsPage';
import SSOProvidersPage from './pages/SSOProvidersPage';
import IdentityBindingsPage from './pages/IdentityBindingsPage';
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
import BillingPaymentsPage from './pages/BillingPaymentsPage';
import BillingInvoicesPage from './pages/BillingInvoicesPage';
import AuditLogsPage from './pages/AuditLogsPage';
import ActivityPage from './pages/user/ActivityPage';
import NotFoundPage from './pages/NotFoundPage';

export default function App() {
  return (
    <Router>
      <SurfaceRouter />
    </Router>
  );
}

function SurfaceRouter() {
  const path = useRoute();
  const moved = MOVED_ADMIN_ROUTES[path];
  if (moved) return <Redirect to={moved} />;
  if (path === '/') return <Redirect to={realmHome('user')} />;
  if (path === '/admin') return <Redirect to={realmHome('admin')} />;
  return isAdminPath(path) ? <AdminSurface path={path} /> : <UserSurface path={path} />;
}

function Redirect({ to }: { to: string }) {
  useEffect(() => {
    replace(to);
  }, [to]);
  return null;
}

function UserSurface({ path }: { path: string }) {
  return (
    <SurfaceProvider realm="user">
      <OrgProvider>
        {path === '/login' ? (
          <UserLoginPage />
        ) : (
          <UserShell>
            <Routes>
              <Route path="/usage" element={<UserUsagePage />} />
              <Route path="/api-keys" element={<UserApiKeysPage />} />
              <Route path="/request-logs" element={<UserRequestLogsPage />} />
              <Route path="/playground" element={<UserPlaygroundPage />} />
              <Route path="/billing" element={<UserBillsPage />} />
              <Route path="/activity" element={<ActivityPage />} />
              <Route path="*" element={<NotFoundPage homePath="/usage" homeLabel="Usage" />} />
            </Routes>
          </UserShell>
        )}
      </OrgProvider>
    </SurfaceProvider>
  );
}

function AdminSurface({ path }: { path: string }) {
  return (
    <SurfaceProvider realm="admin">
      <OrgProvider>
        {path === '/admin/login' ? (
          <AdminLoginPage />
        ) : (
          <AdminShell>
            <Routes>
              <Route path="/admin/organizations" element={<OrganizationsPage />} />
              <Route path="/admin/projects" element={<ProjectsPage />} />
              <Route path="/admin/members" element={<MembersPage />} />
              <Route path="/admin/invitations" element={<InvitationsPage />} />
              <Route path="/admin/sso" element={<SSOProvidersPage />} />
              <Route path="/admin/identity-bindings" element={<IdentityBindingsPage />} />
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
              <Route path="/admin/billing/payments" element={<BillingPaymentsPage />} />
              <Route path="/admin/billing/invoices" element={<BillingInvoicesPage />} />
              <Route path="/admin/audit-logs" element={<AuditLogsPage />} />
              <Route path="*" element={<NotFoundPage homePath="/admin/models" homeLabel="Models" />} />
            </Routes>
          </AdminShell>
        )}
      </OrgProvider>
    </SurfaceProvider>
  );
}
