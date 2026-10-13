import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { Gamepad2, Loader2, CheckCircle2 } from "lucide-react";
import { api, setToken, GET, UserView } from "@/api/client";
import { Button, Card, CardContent, CardHeader, CardTitle, CardDescription, Input, Label } from "@/components/ui/primitives";
import { useAuth } from "@/hooks/AuthContext";

export default function SetupPage() {
  const nav = useNavigate();
  const { refresh } = useAuth();
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (password !== confirm) {
      toast.error("Passwords do not match");
      return;
    }
    if (password.length < 6) {
      toast.error("Password must be at least 6 characters");
      return;
    }
    setBusy(true);
    try {
      const res = await api<{ token: string; user: UserView }>("POST", "/auth/setup", {
        username, password, lang: "en_EN",
      });
      // fallback if setup endpoint is via login after create
      setToken(res.token);
      toast.success(`Welcome, ${res.user.username}!`);
      // refresh auth state to clear needsSetup flag
      await refresh();
      nav("/");
    } catch (err: any) {
      toast.error(err.message || "Setup failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex min-h-screen items-center justify-center bg-background">
      <Card className="w-full max-w-md">
        <CardHeader className="items-center text-center">
          <div className="mx-auto mb-2 flex h-14 w-14 items-center justify-center rounded-2xl bg-primary text-primary-foreground">
            <Gamepad2 className="h-7 w-7" />
          </div>
          <CardTitle className="text-xl">Welcome to Servy</CardTitle>
          <CardDescription>Create the first administrator account</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={submit} className="space-y-4">
            <div className="space-y-1.5">
              <Label>Admin username</Label>
              <Input value={username} onChange={(e) => setUsername(e.target.value)} required minLength={3} />
            </div>
            <div className="space-y-1.5">
              <Label>Password (min 6 chars)</Label>
              <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} required minLength={6} />
            </div>
            <div className="space-y-1.5">
              <Label>Confirm password</Label>
              <Input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required />
            </div>
            <Button type="submit" className="w-full" disabled={busy}>
              {busy ? <Loader2 className="animate-spin" /> : done ? <CheckCircle2 /> : null}
              Create admin & sign in
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
