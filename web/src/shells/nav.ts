// Navigation model for the two console shells (feature-17). Each shell's
// nav items are grouped into categories: the category is rendered as a
// top-level tab bar, and the items within the active category render in the
// left sidebar. This is a pure data model — it does not change routing or
// page logic, only how the existing items are organized and presented.

import type { Icon } from '@phosphor-icons/react';
import {
  Buildings,
  Folder,
  Users,
  Envelope,
  Key,
  LinkSimple,
  Cube,
  Rocket,
  Image,
  ChartLine,
  Tag,
  Receipt,
  UserCircle,
  UserPlus,
  CreditCard,
  FileText,
  Scroll,
  Gauge,
  Cpu,
  PuzzlePiece,
  Bell,
  Globe,
  TextT,
  ArrowsSplit,
  Shuffle,
  Pulse,
  TrendDown,
  SlidersHorizontal,
  ListMagnifyingGlass,
  Play,
  BookOpen,
  DownloadSimple,
  ChartBar,
  ShieldCheck,
  Stack,
  Coins,
  Wrench,
  SquaresFour,
  House,
  ChartDonut,
  Terminal,
  User,
} from '@phosphor-icons/react';

export interface NavItem {
  path: string;
  labelKey: string;
  testid: string;
  icon: Icon;
}

export interface NavCategory {
  /** Stable id used to select the active category. */
  id: string;
  /** i18n key for the category label shown in the top tab bar. */
  labelKey: string;
  /** i18n key for the category's short label (used in the sidebar header). */
  shortLabelKey: string;
  /** Icon for the top tab bar. */
  icon: Icon;
  items: NavItem[];
}

/** Returns the category whose items contain the given path, or the first. */
export function categoryForPath(categories: NavCategory[], path: string): NavCategory {
  return (
    categories.find((c) => c.items.some((i) => path === i.path || path.startsWith(`${i.path}/`))) ||
    categories[0]
  );
}

/** Admin console navigation, grouped into top-level categories. */
export const ADMIN_NAV_CATEGORIES: NavCategory[] = [
  {
    id: 'access',
    labelKey: 'nav.cat.access',
    shortLabelKey: 'nav.cat.accessShort',
    icon: ShieldCheck,
    items: [
      { path: '/admin/users', labelKey: 'nav.users', testid: 'nav-users', icon: Users },
      { path: '/admin/organizations', labelKey: 'nav.organizations', testid: 'nav-organizations', icon: Buildings },
      { path: '/admin/projects', labelKey: 'nav.projects', testid: 'nav-projects', icon: Folder },
      { path: '/admin/members', labelKey: 'nav.members', testid: 'nav-members', icon: Users },
      { path: '/admin/invitations', labelKey: 'nav.invitations', testid: 'nav-invitations', icon: Envelope },
      { path: '/admin/join-requests', labelKey: 'nav.joinRequests', testid: 'nav-join-requests', icon: UserPlus },
      { path: '/admin/profile', labelKey: 'nav.profile', testid: 'nav-profile', icon: UserCircle },
      { path: '/admin/quotas', labelKey: 'nav.quotas', testid: 'nav-quotas', icon: SlidersHorizontal },
      { path: '/admin/sso', labelKey: 'nav.ssoProviders', testid: 'nav-sso-providers', icon: Key },
      { path: '/admin/identity-bindings', labelKey: 'nav.identityBindings', testid: 'nav-identity-bindings', icon: LinkSimple },
    ],
  },
  {
    id: 'inference',
    labelKey: 'nav.cat.inference',
    shortLabelKey: 'nav.cat.inferenceShort',
    icon: Stack,
    items: [
      { path: '/admin/models', labelKey: 'nav.models', testid: 'nav-models', icon: Cube },
      { path: '/admin/inference-services', labelKey: 'nav.inferenceServices', testid: 'nav-inference-services', icon: Rocket },
      { path: '/admin/clusters', labelKey: 'nav.clusters', testid: 'nav-clusters', icon: Globe },
      { path: '/admin/providers', labelKey: 'nav.providers', testid: 'nav-providers', icon: Cube },
      { path: '/admin/deployments', labelKey: 'nav.deployments', testid: 'nav-deployments', icon: Scroll },
      { path: '/admin/images', labelKey: 'nav.images', testid: 'nav-images', icon: Image },
      { path: '/admin/engine-templates', labelKey: 'nav.engineTemplates', testid: 'nav-engine-templates', icon: SlidersHorizontal },
    ],
  },
  {
    id: 'billing',
    labelKey: 'nav.cat.billing',
    shortLabelKey: 'nav.cat.billingShort',
    icon: Coins,
    items: [
      { path: '/admin/usage', labelKey: 'nav.usage', testid: 'nav-usage', icon: ChartLine },
      { path: '/admin/usage/keys', labelKey: 'nav.usageKeys', testid: 'nav-usage-keys', icon: Key },
      { path: '/admin/cost', labelKey: 'nav.cost', testid: 'nav-cost', icon: ChartLine },
      { path: '/admin/forecast', labelKey: 'nav.forecast', testid: 'nav-forecast', icon: ChartLine },
      { path: '/admin/optimization', labelKey: 'nav.optimization', testid: 'nav-optimization', icon: TrendDown },
      { path: '/admin/pricing', labelKey: 'nav.pricing', testid: 'nav-pricing', icon: Tag },
      { path: '/admin/billing', labelKey: 'nav.bills', testid: 'nav-bills', icon: Receipt },
      { path: '/admin/billing/accounts', labelKey: 'nav.accounts', testid: 'nav-accounts', icon: UserCircle },
      { path: '/admin/billing/payments', labelKey: 'nav.payments', testid: 'nav-payments', icon: CreditCard },
      { path: '/admin/billing/invoices', labelKey: 'nav.invoices', testid: 'nav-invoices', icon: FileText },
      { path: '/admin/billing/reports', labelKey: 'nav.billingReports', testid: 'nav-billing-reports', icon: FileText },
    ],
  },
  {
    id: 'ops',
    labelKey: 'nav.cat.ops',
    shortLabelKey: 'nav.cat.opsShort',
    icon: Wrench,
    items: [
      { path: '/admin/autoscaling', labelKey: 'nav.autoscaling', testid: 'nav-autoscaling', icon: Gauge },
      { path: '/admin/accelerators', labelKey: 'nav.accelerators', testid: 'nav-accelerators', icon: Cpu },
      { path: '/admin/vgpu-pools', labelKey: 'nav.vgpuPools', testid: 'nav-vgpu-pools', icon: Cpu },
      { path: '/admin/faults', labelKey: 'nav.faults', testid: 'nav-faults', icon: Gauge },
      { path: '/admin/compatibility', labelKey: 'nav.compatibility', testid: 'nav-compatibility', icon: PuzzlePiece },
      { path: '/admin/load-tests', labelKey: 'nav.loadTests', testid: 'nav-load-tests', icon: Gauge },
      { path: '/admin/routing-policies', labelKey: 'nav.routingPolicies', testid: 'nav-routing-policies', icon: ArrowsSplit },
      { path: '/admin/routing-groups', labelKey: 'nav.routingGroups', testid: 'nav-routing-groups', icon: Shuffle },
    ],
  },
  {
    id: 'platform',
    labelKey: 'nav.cat.platform',
    shortLabelKey: 'nav.cat.platformShort',
    icon: SquaresFour,
    items: [
      { path: '/admin/webhooks', labelKey: 'nav.webhooks', testid: 'nav-webhooks', icon: LinkSimple },
      { path: '/admin/notifications', labelKey: 'nav.notifications', testid: 'nav-notifications', icon: Bell },
      { path: '/admin/observability', labelKey: 'nav.observability', testid: 'nav-observability', icon: ChartLine },
      { path: '/admin/traces', labelKey: 'nav.traces', testid: 'nav-traces', icon: ChartLine },
      { path: '/admin/errors', labelKey: 'nav.errors', testid: 'nav-errors', icon: ChartLine },
      { path: '/admin/batch', labelKey: 'nav.batch', testid: 'nav-batch', icon: Cube },
      { path: '/admin/prompts', labelKey: 'nav.prompts', testid: 'nav-prompts', icon: TextT },
      { path: '/admin/status', labelKey: 'nav.status', testid: 'nav-status', icon: Gauge },
      { path: '/admin/settings', labelKey: 'nav.gatewaySettings', testid: 'nav-gateway-settings', icon: Wrench },
    ],
  },
  {
    id: 'governance',
    labelKey: 'nav.cat.governance',
    shortLabelKey: 'nav.cat.governanceShort',
    icon: ShieldCheck,
    items: [
      { path: '/admin/audit-logs', labelKey: 'nav.auditLogs', testid: 'nav-audit-logs', icon: Scroll },
    ],
  },
];

/** End-user console navigation, grouped into top-level categories. */
export const USER_NAV_CATEGORIES: NavCategory[] = [
  {
    id: 'start',
    labelKey: 'nav.cat.start',
    shortLabelKey: 'nav.cat.startShort',
    icon: House,
    items: [
      { path: '/quickstart', labelKey: 'nav.quickstart', testid: 'user-nav-quickstart', icon: Rocket },
      { path: '/docs', labelKey: 'nav.apiDocs', testid: 'user-nav-api-docs', icon: BookOpen },
    ],
  },
  {
    id: 'usage',
    labelKey: 'nav.cat.usage',
    shortLabelKey: 'nav.cat.usageShort',
    icon: ChartDonut,
    items: [
      { path: '/usage', labelKey: 'nav.usage', testid: 'user-nav-usage', icon: ChartLine },
      { path: '/usage/keys', labelKey: 'nav.usageKeys', testid: 'user-nav-usage-keys', icon: Key },
      { path: '/cost', labelKey: 'nav.cost', testid: 'user-nav-cost', icon: ChartLine },
      { path: '/forecast', labelKey: 'nav.forecast', testid: 'user-nav-forecast', icon: ChartLine },
      { path: '/quotas', labelKey: 'nav.quotas', testid: 'user-nav-quotas', icon: Gauge },
      { path: '/optimization', labelKey: 'nav.optimization', testid: 'user-nav-optimization', icon: TrendDown },
    ],
  },
  {
    id: 'api',
    labelKey: 'nav.cat.api',
    shortLabelKey: 'nav.cat.apiShort',
    icon: Terminal,
    items: [
      { path: '/api-keys', labelKey: 'nav.apiKeys', testid: 'user-nav-api-keys', icon: Key },
      { path: '/request-logs', labelKey: 'nav.requestLogs', testid: 'user-nav-request-logs', icon: ListMagnifyingGlass },
      { path: '/playground', labelKey: 'nav.playground', testid: 'user-nav-playground', icon: Play },
    ],
  },
  {
    id: 'workloads',
    labelKey: 'nav.cat.workloads',
    shortLabelKey: 'nav.cat.workloadsShort',
    icon: Stack,
    items: [
      { path: '/batch', labelKey: 'nav.batch', testid: 'user-nav-batch', icon: Cube },
      { path: '/prompts', labelKey: 'nav.prompts', testid: 'user-nav-prompts', icon: TextT },
      { path: '/evaluations', labelKey: 'nav.evaluations', testid: 'user-nav-evaluations', icon: ChartBar },
    ],
  },
  {
    id: 'billing',
    labelKey: 'nav.cat.billing',
    shortLabelKey: 'nav.cat.billingShort',
    icon: Coins,
    items: [
      { path: '/billing', labelKey: 'nav.billing', testid: 'user-nav-billing', icon: Receipt },
      { path: '/billing/reports', labelKey: 'nav.billingReports', testid: 'user-nav-billing-reports', icon: FileText },
    ],
  },
  {
    id: 'account',
    labelKey: 'nav.cat.account',
    shortLabelKey: 'nav.cat.accountShort',
    icon: User,
    items: [
      { path: '/my-org', labelKey: 'nav.myOrganizations', testid: 'user-nav-my-org', icon: Buildings },
      { path: '/my-projects', labelKey: 'nav.myProjects', testid: 'user-nav-my-projects', icon: Folder },
      { path: '/account/profile', labelKey: 'nav.profile', testid: 'user-nav-profile', icon: UserCircle },
      { path: '/my-requests', labelKey: 'nav.myRequests', testid: 'user-nav-my-requests', icon: UserPlus },
      { path: '/activity', labelKey: 'nav.activity', testid: 'user-nav-activity', icon: Pulse },
      { path: '/webhooks', labelKey: 'nav.webhooks', testid: 'user-nav-webhooks', icon: LinkSimple },
      { path: '/notifications', labelKey: 'nav.notifications', testid: 'user-nav-notifications', icon: Bell },
      { path: '/models', labelKey: 'nav.models', testid: 'user-nav-models', icon: Cube },
      { path: '/traces', labelKey: 'nav.traces', testid: 'user-nav-traces', icon: ChartLine },
      { path: '/errors', labelKey: 'nav.errors', testid: 'user-nav-errors', icon: ChartLine },
      { path: '/account/export', labelKey: 'nav.dataExport', testid: 'user-nav-data-export', icon: DownloadSimple },
    ],
  },
];
// ---- Scope-aware admin navigation (feature #54) ------------------------
// The admin console has three management scopes: platform, organization
// and project. Each scope sees only its own functions. A multi-role user
// chooses the scope at /admin; the shell renders the matching categories.

// platformHiddenItems are the pages a platform administrator does not
// use: project management (projects belong to organizations) and the
// per-organization member/invitation/join-request pipelines, which
// organization administrators own. The platform keeps the user directory:
// a platform administrator manages users and places them into any
// organization.
const platformHiddenItems = new Set([
  '/admin/projects',
  '/admin/members',
  '/admin/invitations',
  '/admin/join-requests',
]);

/** Platform console navigation: the operator surface minus project
 * management and the per-organization membership pipelines. */
export function platformNavCategories(): NavCategory[] {
  return ADMIN_NAV_CATEGORIES.map((c) => ({
    ...c,
    items: c.items.filter((i) => !platformHiddenItems.has(i.path)),
  })).filter((c) => c.items.length > 0);
}

/** Organization console navigation: metadata, projects, roster,
 * invitations, join requests and project quotas. No inference/platform
 * pages. */
export function organizationNavCategories(): NavCategory[] {
  return [
    {
      id: 'org',
      labelKey: 'nav.cat.org',
      shortLabelKey: 'nav.cat.orgShort',
      icon: Buildings,
      items: [
        { path: '/admin/org-settings', labelKey: 'nav.orgSettings', testid: 'nav-org-settings', icon: Buildings },
        { path: '/admin/projects', labelKey: 'nav.projects', testid: 'nav-projects', icon: Folder },
        { path: '/admin/members', labelKey: 'nav.members', testid: 'nav-members', icon: Users },
        { path: '/admin/invitations', labelKey: 'nav.invitations', testid: 'nav-invitations', icon: Envelope },
        { path: '/admin/join-requests', labelKey: 'nav.joinRequests', testid: 'nav-join-requests', icon: UserPlus },
        { path: '/admin/project-quotas', labelKey: 'nav.memberQuotas', testid: 'nav-member-quotas', icon: SlidersHorizontal },
      ],
    },
    {
      id: 'orgbilling',
      labelKey: 'nav.cat.billing',
      shortLabelKey: 'nav.cat.billingShort',
      icon: Coins,
      items: [
        { path: '/admin/usage', labelKey: 'nav.usage', testid: 'nav-usage', icon: ChartLine },
        { path: '/admin/billing', labelKey: 'nav.bills', testid: 'nav-bills', icon: Receipt },
      ],
    },
  ];
}

/** Project console navigation: project members, join requests and the
 * project quota. */
export function projectNavCategories(): NavCategory[] {
  return [
    {
      id: 'project',
      labelKey: 'nav.cat.project',
      shortLabelKey: 'nav.cat.projectShort',
      icon: Folder,
      items: [
        { path: '/admin/project-members', labelKey: 'nav.projectMembers', testid: 'nav-project-members', icon: Users },
        { path: '/admin/join-requests', labelKey: 'nav.joinRequests', testid: 'nav-join-requests', icon: UserPlus },
        { path: '/admin/project-quotas', labelKey: 'nav.memberQuotasShort', testid: 'nav-member-quotas', icon: SlidersHorizontal },
      ],
    },
  ];
}

/** navCategoriesForScope returns the categories of a scope type. */
export function navCategoriesForScope(scopeType: string | undefined): NavCategory[] {
  switch (scopeType) {
    case 'organization':
      return organizationNavCategories();
    case 'project':
      return projectNavCategories();
    case 'platform':
      return platformNavCategories();
    default:
      return platformNavCategories();
  }
}
