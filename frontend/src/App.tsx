import { useEffect, useState } from "react";
import { BrowserRouter, Routes, Route, Navigate, useLocation } from "react-router-dom";
import { Toaster } from "sonner";
import { AuthProvider, useAuth } from "@/hooks/AuthContext";
import { TooltipProvider } from "@/components/ui/overlays";
import { Skeleton } from "@/components/ui/primitives";

import LoginPage from "@/pages/Login";
import SetupPage from "@/pages/Setup";
import PanelLayout from "@/pages/PanelLayout";
import Dashboard from "@/pages/Dashboard";
import ServersListPage from "@/pages/ServersList";
import CreateServerPage from "@/pages/CreateServer";
import ServerDetail from "@/pages/ServerDetail";
import UsersPage from "@/pages/Users";
import RolesPage from "@/pages/Roles";
import ProfilePage from "@/pages/Profile";
import PanelConfigPage from "@/pages/PanelConfig";
import LogsPage from "@/pages/Logs";
import NodesPage from "@/pages/Nodes";

function Protected({ children }: { children: any }) {
  const { user, loading, needsSetup } = useAuth();
  const loc = useLocation();
  if (loading) {
    return (
      <div className="flex h-screen items-center justify-center bg-background">
        <div className="space-y-4 w-64">
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-4 w-3/4" />
          <Skeleton className="h-4 w-1/2" />
        </div>
      </div>
    );
  }
  if (needsSetup) return <Navigate to="/setup" replace />;
  if (!user) return <Navigate to="/login" replace state={{ from: loc.pathname }} />;
  return children;
}

function PublicOnly({ children }: { children: any }) {
  const { user, loading, needsSetup } = useAuth();
  const loc = useLocation();
  if (loading) return null;
  if (needsSetup && loc.pathname !== "/setup") return <Navigate to="/setup" replace />;
  if (user) return <Navigate to="/" replace />;
  return children;
}

export default function App() {
  return (
    <AuthProvider>
      <TooltipProvider delayDuration={200}>
        <BrowserRouter>
          <Routes>
            <Route path="/setup" element={<SetupPage />} />
            <Route path="/login" element={<PublicOnly><LoginPage /></PublicOnly>} />
            <Route path="/" element={<Protected><PanelLayout /></Protected>}>
              <Route index element={<Dashboard />} />
              <Route path="servers" element={<ServersListPage />} />
              <Route path="servers/new" element={<CreateServerPage />} />
              <Route path="servers/:id" element={<ServerDetail />} />
              <Route path="users" element={<UsersPage />} />
              <Route path="roles" element={<RolesPage />} />
              <Route path="profile" element={<ProfilePage />} />
              <Route path="config" element={<PanelConfigPage />} />
              <Route path="logs" element={<LogsPage />} />
              <Route path="nodes" element={<NodesPage />} />
            </Route>
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </BrowserRouter>
        <Toaster theme="dark" position="bottom-right" richColors />
      </TooltipProvider>
    </AuthProvider>
  );
}
