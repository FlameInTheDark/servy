import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import { Gamepad2, Loader2, ShieldCheck } from "lucide-react";
import { useAuth } from "@/hooks/AuthContext";
import { Button } from "@/components/ui/primitives";
import { Input, Label, Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/primitives";

export default function LoginPage() {
  const { login } = useAuth();
  const nav = useNavigate();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [totp, setTotp] = useState("");
  const [mfaNeeded, setMfaNeeded] = useState(false);
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      await login(username, password, totp);
      nav("/");
    } catch (err: any) {
      if (err.code === "MFA_REQUIRED") {
        setMfaNeeded(true);
        toast.info("Enter your 2FA code");
      } else if (err.code === "INVALID_MFA") {
        toast.error("Invalid 2FA code");
      } else if (err.code === "LOCKED_OUT") {
        toast.error("Account locked — try again in 10 minutes", { duration: 8000 });
      } else {
        toast.error(err.message || "Login failed");
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="relative flex min-h-screen items-center justify-center bg-background overflow-hidden">
      {/* subtle grid backdrop */}
      <div
        className="absolute inset-0 opacity-[0.15]"
        style={{
          backgroundImage:
            "linear-gradient(hsl(var(--primary) / 0.25) 1px, transparent 1px), linear-gradient(90deg, hsl(var(--primary) / 0.25) 1px, transparent 1px)",
          backgroundSize: "44px 44px",
          maskImage: "radial-gradient(ellipse 70% 60% at 50% 40%, black, transparent)",
        }}
      />
      <div className="absolute -top-40 left-1/2 h-96 w-[42rem] -translate-x-1/2 rounded-full bg-primary/10 blur-3xl" />

      <Card className="relative w-full max-w-sm border-border/60">
        <CardHeader className="items-center text-center">
          <div className="mx-auto mb-2 flex h-14 w-14 items-center justify-center rounded-2xl bg-primary text-primary-foreground shadow-lg shadow-primary/20">
            <Gamepad2 className="h-7 w-7" />
          </div>
          <CardTitle className="text-xl">Servy</CardTitle>
          <CardDescription>Game server management panel</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={submit} className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="username">Username</Label>
              <Input
                id="username"
                autoComplete="username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                required
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="password">Password</Label>
              <Input
                id="password"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </div>
            {mfaNeeded && (
              <div className="space-y-1.5">
                <Label htmlFor="totp" className="flex items-center gap-1.5">
                  <ShieldCheck className="h-3.5 w-3.5" /> 2FA Code
                </Label>
                <Input
                  id="totp"
                  inputMode="numeric"
                  placeholder="123456"
                  value={totp}
                  onChange={(e) => setTotp(e.target.value)}
                  autoFocus
                />
              </div>
            )}
            <Button type="submit" className="w-full" disabled={busy}>
              {busy && <Loader2 className="animate-spin" />}
              Sign in
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
