import { useState } from "react";
import { toast } from "sonner";
import { ShieldCheck, KeyRound, Copy, Save } from "lucide-react";
import { GET, POST, DEL } from "@/api/client";
import { useAuth } from "@/hooks/AuthContext";
import { Button, Card, CardContent, CardHeader, CardTitle, CardDescription, Input, Label, Badge } from "@/components/ui/primitives";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/overlays";

export default function ProfilePage() {
  const { user, refresh } = useAuth();
  const [pw, setPw] = useState({ current: "", next: "", confirm: "" });
  const [totps, setTotps] = useState<any[]>([]);
  const [newTotp, setNewTotp] = useState<{ id: string; secret: string; url: string } | null>(null);
  const [verifyCode, setVerifyCode] = useState("");
  const [recovery, setRecovery] = useState<string[] | null>(null);
  const [keys, setKeys] = useState<any[]>([]);
  const [createdToken, setCreatedToken] = useState<string | null>(null);

  useState(() => {
    if (user) {
      GET<any[]>(`/users/${user.user_id}/totp`).then(setTotps).catch(() => {});
      GET<any[]>(`/users/${user.user_id}/key`).then(setKeys).catch(() => {});
    }
  });
  if (!user) return null;

  const changePw = async () => {
    if (pw.next !== pw.confirm) { toast.error("Passwords don't match"); return; }
    try {
      await POST(`/users/${user.user_id}/password`, { password: pw.next });
      toast.success("Password changed — please sign in again");
      setPw({ current: "", next: "", confirm: "" });
    } catch (e: any) { toast.error(e.message); }
  };

  const startTotp = async () => {
    const t = await POST<any>(`/users/${user.user_id}/totp`, { name: "Authenticator" });
    setNewTotp({ id: t.id, secret: t.secret, url: t.otpauth_url });
  };

  const verifyTotp = async () => {
    try {
      await POST(`/totp/${newTotp!.id}/verify`, { code: verifyCode });
      toast.success("2FA enabled");
      setNewTotp(null);
      setVerifyCode("");
      setTotps(await GET(`/users/${user.user_id}/totp`));
      const rec = await POST<any>("/totp/recovery/renew");
      setRecovery((rec as any).recovery_codes || []);
    } catch (e: any) {
      toast.error(e.message);
    }
  };

  return (
    <div className="p-6 max-w-3xl mx-auto space-y-6">
      <div>
        <h1 className="text-2xl font-bold">Profile</h1>
        <p className="text-sm text-muted-foreground">{user.username} · #{user.user_id}{user.superuser ? " · superuser" : ""}</p>
      </div>

      {/* password */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Change password</CardTitle>
          <CardDescription>Changing your password invalidates all sessions</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-3 sm:grid-cols-3">
          <div className="space-y-1.5">
            <Label>New password</Label>
            <Input type="password" value={pw.next} onChange={(e) => setPw({ ...pw, next: e.target.value })} />
          </div>
          <div className="space-y-1.5">
            <Label>Confirm</Label>
            <Input type="password" value={pw.confirm} onChange={(e) => setPw({ ...pw, confirm: e.target.value })} />
          </div>
          <div className="flex items-end">
            <Button onClick={changePw} disabled={pw.next.length < 6}><Save /> Update</Button>
          </div>
        </CardContent>
      </Card>

      {/* 2FA */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <ShieldCheck className="h-4 w-4 text-primary" /> Two-factor authentication
          </CardTitle>
          <CardDescription>TOTP authenticator apps (Google/Authy/1Password…)</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          {totps.length === 0 ? (
            <div className="flex items-center justify-between rounded-lg border border-dashed p-4">
              <span className="text-sm text-muted-foreground">No 2FA method configured</span>
              <Button size="sm" variant="outline" onClick={startTotp}>Enable 2FA</Button>
            </div>
          ) : totps.map((t) => (
            <div key={t.id} className="flex items-center justify-between rounded-lg border p-3">
              <div className="flex items-center gap-2 text-sm">
                <ShieldCheck className="h-4 w-4 text-success" /> {t.name} <Badge variant="success">active</Badge>
              </div>
              <Button size="sm" variant="ghost" onClick={async () => {
                await DEL(`/totp/${t.id}`);
                setTotps(await GET(`/users/${user.user_id}/totp`));
              }}>Remove</Button>
            </div>
          ))}
          {recovery && (
            <div className="rounded-lg border border-warning/40 bg-warning/10 p-4">
              <div className="text-sm font-medium text-warning">Recovery codes (store safely — shown once)</div>
              <div className="mt-2 grid grid-cols-3 gap-1.5 font-mono text-xs">
                {recovery.map((c) => <code key={c} className="rounded bg-black/30 px-2 py-1">{c}</code>)}
              </div>
              <Button size="sm" variant="outline" className="mt-2" onClick={async () => {
                const rec = await POST<any>("/totp/recovery/renew");
                setRecovery(rec.recovery_codes || []);
              }}><Copy /> Regenerate</Button>
            </div>
          )}
        </CardContent>
      </Card>

      {/* API keys */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <KeyRound className="h-4 w-4 text-primary" /> API keys
          </CardTitle>
          <CardDescription>Bearer tokens for the REST API (/api/v1)</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          {createdToken && (
            <div className="rounded-md border border-warning/40 bg-warning/10 p-3">
              <div className="text-xs font-medium text-warning">Copy this token now:</div>
              <code className="mt-1 block break-all font-mono text-xs">{createdToken}</code>
            </div>
          )}
          {keys.length === 0 ? (
            <div className="text-sm text-muted-foreground">No API keys</div>
          ) : keys.map((k) => (
            <div key={k.token_id} className="flex items-center justify-between rounded-lg border p-3">
              <div>
                <div className="text-sm font-medium">{k.name}</div>
                <div className="text-xs text-muted-foreground">#{k.token_id} · {k.full_access ? "full access" : "restricted"}</div>
              </div>
              <Button size="sm" variant="ghost" onClick={async () => {
                await DEL(`/users/${user.user_id}/key/${k.token_id}`);
                setKeys(await GET(`/users/${user.user_id}/key`));
              }}>Remove</Button>
            </div>
          ))}
          <form className="flex gap-2" onSubmit={async (e) => {
            e.preventDefault();
            const name = (e.currentTarget.elements.namedItem("keyname") as HTMLInputElement).value;
            if (!name) return;
            try {
              const k = await POST<any>(`/users/${user.user_id}/key`, { name, full_access: false });
              setCreatedToken(k.token);
              setKeys(await GET(`/users/${user.user_id}/key`));
              (e.currentTarget as HTMLFormElement).reset();
            } catch (err: any) { toast.error(err.message); }
          }}>
            <Input name="keyname" placeholder="New key name" />
            <Button type="submit" size="sm">Create</Button>
          </form>
        </CardContent>
      </Card>

      {/* totp setup dialog */}
      <Dialog open={!!newTotp} onOpenChange={(o) => !o && setNewTotp(null)}>
        <DialogContent>
          <DialogHeader><DialogTitle>Set up 2FA</DialogTitle></DialogHeader>
          <div className="space-y-3">
            <p className="text-sm text-muted-foreground">
              Add this secret to your authenticator app, then enter the 6-digit code.
            </p>
            <code className="block break-all rounded-md border bg-black/40 p-3 font-mono text-sm">{newTotp?.secret}</code>
            <div className="flex gap-2">
              <Input placeholder="123456" value={verifyCode} onChange={(e) => setVerifyCode(e.target.value)} inputMode="numeric" />
              <Button onClick={verifyTotp} disabled={verifyCode.length < 6}>Verify</Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
