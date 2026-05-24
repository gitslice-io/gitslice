import { BrowserRouter, Routes, Route, Navigate } from "react-router-dom";
import { Layout } from "./components/Layout";
import { LoginPage } from "./pages/LoginPage";
import { HomePage } from "./pages/HomePage";
import { SourcePage } from "./pages/SourcePage";
import { SliceListPage } from "./pages/SliceListPage";
import { SliceDetailPage } from "./pages/SliceDetailPage";
import { SliceSettingsPage } from "./pages/SliceSettingsPage";
import { ChangesetLookupPage } from "./pages/ChangesetLookupPage";
import { CreateChangesetPage } from "./pages/CreateChangesetPage";
import { ChangesetDetailPage } from "./pages/ChangesetDetailPage";
import "./App.css";

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route
          path="*"
          element={
            <Layout>
              <Routes>
                <Route path="/" element={<HomePage />} />
                <Route path="/source" element={<SourcePage />} />
                <Route path="/source/:account/*" element={<SourcePage />} />
                <Route path="/slices" element={<SliceListPage />} />
                <Route path="/slices/:id" element={<SliceDetailPage />} />
                <Route
                  path="/slices/:id/settings"
                  element={<SliceSettingsPage />}
                />
                <Route path="/changesets" element={<ChangesetLookupPage />} />
                <Route
                  path="/changesets/new"
                  element={<CreateChangesetPage />}
                />
                <Route
                  path="/changesets/:id"
                  element={<ChangesetDetailPage />}
                />
                <Route path="*" element={<Navigate to="/" replace />} />
              </Routes>
            </Layout>
          }
        />
      </Routes>
    </BrowserRouter>
  );
}
