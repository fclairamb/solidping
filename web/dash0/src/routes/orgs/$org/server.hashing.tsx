import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { AlertCircle, Check, Info, Loader2 } from "lucide-react";
import { ApiError } from "@/api/client";
import { useSystemParameters, useSetSystemParameter } from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/server/hashing")({
  component: HashingSettingsPage,
});

// Canonical auth.password.* system-parameter keys. These MUST match the backend
// systemconfig KEY_* constants (server/internal/systemconfig/systemconfig.go);
// the TestKnownPasswordKeys test pins the backend half of this contract.
const KEY_ALGORITHM = "auth.password.algorithm";
const KEY_ARGON2_MEMORY = "auth.password.argon2.memory";
const KEY_ARGON2_TIME = "auth.password.argon2.time";
const KEY_ARGON2_THREADS = "auth.password.argon2.threads";
const KEY_ARGON2_KEY_LENGTH = "auth.password.argon2.key_length";
const KEY_ARGON2_SALT_LENGTH = "auth.password.argon2.salt_length";
const KEY_BCRYPT_COST = "auth.password.bcrypt.cost";
const KEY_REHASH_ON_LOGIN = "auth.password.rehash_on_login";

type Algorithm = "argon2id" | "bcrypt";

// Recommended argon2id profiles (OWASP-aligned) used by the preset buttons.
const ARGON2_PRESETS: { id: string; memory: string; time: string; threads: string }[] = [
  { id: "high", memory: "65536", time: "3", threads: "4" },
  { id: "owasp", memory: "19456", time: "2", threads: "1" },
  { id: "low", memory: "9216", time: "4", threads: "1" },
];

// HashingInitialValues holds the form's seed values, extracted from the loaded
// system parameters. Passing them as props lets the inner form initialize its
// useState directly (no setState-in-effect), and the params-keyed remount in the
// loader re-seeds the form if the saved values change underneath it.
interface HashingInitialValues {
  algorithm: Algorithm;
  argon2Memory: string;
  argon2Time: string;
  argon2Threads: string;
  argon2KeyLength: string;
  argon2SaltLength: string;
  bcryptCost: string;
  rehashOnLogin: boolean;
}

// HashingSettingsPage loads the system parameters, then renders the form once,
// initialized from those values. While loading it shows a spinner.
function HashingSettingsPage() {
  const { data: params, isLoading } = useSystemParameters();

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
      </div>
    );
  }

  const list = params ?? [];
  const get = (key: string) => list.find((p) => p.key === key)?.value;
  const toStr = (v: unknown) => (v == null ? "" : String(v));
  const algo = get(KEY_ALGORITHM);
  const rehash = get(KEY_REHASH_ON_LOGIN);

  const initial: HashingInitialValues = {
    algorithm: algo === "bcrypt" ? "bcrypt" : "argon2id",
    argon2Memory: toStr(get(KEY_ARGON2_MEMORY)),
    argon2Time: toStr(get(KEY_ARGON2_TIME)),
    argon2Threads: toStr(get(KEY_ARGON2_THREADS)),
    argon2KeyLength: toStr(get(KEY_ARGON2_KEY_LENGTH)),
    argon2SaltLength: toStr(get(KEY_ARGON2_SALT_LENGTH)),
    bcryptCost: toStr(get(KEY_BCRYPT_COST)),
    rehashOnLogin: rehash == null ? true : Boolean(rehash),
  };

  return <HashingForm initial={initial} />;
}

// HashingForm renders the editable hashing settings, seeded once from `initial`.
function HashingForm({ initial }: { initial: HashingInitialValues }) {
  const { t } = useTranslation(["server", "common"]);
  const setParam = useSetSystemParameter();

  const [algorithm, setAlgorithm] = useState<Algorithm>(initial.algorithm);
  const [argon2Memory, setArgon2Memory] = useState(initial.argon2Memory);
  const [argon2Time, setArgon2Time] = useState(initial.argon2Time);
  const [argon2Threads, setArgon2Threads] = useState(initial.argon2Threads);
  const [argon2KeyLength, setArgon2KeyLength] = useState(initial.argon2KeyLength);
  const [argon2SaltLength, setArgon2SaltLength] = useState(initial.argon2SaltLength);
  const [bcryptCost, setBcryptCost] = useState(initial.bcryptCost);
  const [rehashOnLogin, setRehashOnLogin] = useState(initial.rehashOnLogin);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  const applyPreset = (preset: (typeof ARGON2_PRESETS)[number]) => {
    setArgon2Memory(preset.memory);
    setArgon2Time(preset.time);
    setArgon2Threads(preset.threads);
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSaved(false);

    // An empty numeric field means "inherit the server default": omit the key so
    // the backend zero-fills it (resolveArgon2Params). Only send populated fields.
    const numericWrite = (key: string, raw: string) => {
      const trimmed = raw.trim();
      if (trimmed === "") return null;
      const n = Number(trimmed);
      if (!Number.isFinite(n)) return null;
      return setParam.mutateAsync({ key, value: n, secret: false });
    };

    const writes: Promise<unknown>[] = [
      setParam.mutateAsync({ key: KEY_ALGORITHM, value: algorithm, secret: false }),
      setParam.mutateAsync({ key: KEY_REHASH_ON_LOGIN, value: rehashOnLogin, secret: false }),
    ];

    if (algorithm === "argon2id") {
      for (const w of [
        numericWrite(KEY_ARGON2_MEMORY, argon2Memory),
        numericWrite(KEY_ARGON2_TIME, argon2Time),
        numericWrite(KEY_ARGON2_THREADS, argon2Threads),
        numericWrite(KEY_ARGON2_KEY_LENGTH, argon2KeyLength),
        numericWrite(KEY_ARGON2_SALT_LENGTH, argon2SaltLength),
      ]) {
        if (w) writes.push(w);
      }
    } else {
      const w = numericWrite(KEY_BCRYPT_COST, bcryptCost);
      if (w) writes.push(w);
    }

    try {
      await Promise.all(writes);
      setSaved(true);
      setTimeout(() => setSaved(false), 3000);
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
      } else {
        setError(t("server:unexpectedError"));
      }
    }
  };

  return (
    <form onSubmit={handleSave} className="space-y-4" data-testid="hashing-form">
      {error && (
        <Alert variant="destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertDescription data-testid="hashing-error">{error}</AlertDescription>
        </Alert>
      )}
      {saved && (
        <Alert>
          <Check className="h-4 w-4" />
          <AlertDescription data-testid="hashing-saved">{t("server:saved")}</AlertDescription>
        </Alert>
      )}

      <Alert>
        <Info className="h-4 w-4" />
        <AlertDescription>{t("server:hashing.restartNotice")}</AlertDescription>
      </Alert>

      <Card>
        <CardHeader>
          <CardTitle>{t("server:hashing.title")}</CardTitle>
          <CardDescription>{t("server:hashing.description")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="algorithm">{t("server:hashing.algorithm")}</Label>
            <Select
              value={algorithm}
              onValueChange={(v) => setAlgorithm(v as Algorithm)}
              disabled={setParam.isPending}
            >
              <SelectTrigger id="algorithm" data-testid="hashing-algorithm">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="argon2id">{t("server:hashing.algorithms.argon2id")}</SelectItem>
                <SelectItem value="bcrypt">{t("server:hashing.algorithms.bcrypt")}</SelectItem>
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">{t("server:hashing.algorithmHelp")}</p>
          </div>

          {algorithm === "argon2id" ? (
            <div className="space-y-4">
              <div className="space-y-2">
                <Label>{t("server:hashing.presets.label")}</Label>
                <div className="flex flex-wrap gap-2">
                  {ARGON2_PRESETS.map((preset) => (
                    <Button
                      key={preset.id}
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={() => applyPreset(preset)}
                      disabled={setParam.isPending}
                      data-testid={`hashing-preset-${preset.id}`}
                    >
                      {t(`server:hashing.presets.${preset.id}`)}
                    </Button>
                  ))}
                </div>
                <p className="text-xs text-muted-foreground">{t("server:hashing.presets.help")}</p>
              </div>

              <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                <div className="space-y-2">
                  <Label htmlFor="argon2Memory">{t("server:hashing.argon2.memory")}</Label>
                  <Input
                    id="argon2Memory"
                    type="number"
                    min="8192"
                    placeholder="65536"
                    value={argon2Memory}
                    onChange={(e) => setArgon2Memory(e.target.value)}
                    disabled={setParam.isPending}
                    data-testid="hashing-argon2-memory"
                  />
                  <p className="text-xs text-muted-foreground">{t("server:hashing.argon2.memoryHelp")}</p>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="argon2Time">{t("server:hashing.argon2.time")}</Label>
                  <Input
                    id="argon2Time"
                    type="number"
                    min="1"
                    placeholder="3"
                    value={argon2Time}
                    onChange={(e) => setArgon2Time(e.target.value)}
                    disabled={setParam.isPending}
                    data-testid="hashing-argon2-time"
                  />
                  <p className="text-xs text-muted-foreground">{t("server:hashing.argon2.timeHelp")}</p>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="argon2Threads">{t("server:hashing.argon2.threads")}</Label>
                  <Input
                    id="argon2Threads"
                    type="number"
                    min="1"
                    max="255"
                    placeholder="4"
                    value={argon2Threads}
                    onChange={(e) => setArgon2Threads(e.target.value)}
                    disabled={setParam.isPending}
                    data-testid="hashing-argon2-threads"
                  />
                  <p className="text-xs text-muted-foreground">{t("server:hashing.argon2.threadsHelp")}</p>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="argon2KeyLength">{t("server:hashing.argon2.keyLength")}</Label>
                  <Input
                    id="argon2KeyLength"
                    type="number"
                    min="16"
                    placeholder="32"
                    value={argon2KeyLength}
                    onChange={(e) => setArgon2KeyLength(e.target.value)}
                    disabled={setParam.isPending}
                    data-testid="hashing-argon2-key-length"
                  />
                  <p className="text-xs text-muted-foreground">{t("server:hashing.argon2.keyLengthHelp")}</p>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="argon2SaltLength">{t("server:hashing.argon2.saltLength")}</Label>
                  <Input
                    id="argon2SaltLength"
                    type="number"
                    min="8"
                    placeholder="16"
                    value={argon2SaltLength}
                    onChange={(e) => setArgon2SaltLength(e.target.value)}
                    disabled={setParam.isPending}
                    data-testid="hashing-argon2-salt-length"
                  />
                  <p className="text-xs text-muted-foreground">{t("server:hashing.argon2.saltLengthHelp")}</p>
                </div>
              </div>
            </div>
          ) : (
            <div className="space-y-2">
              <Label htmlFor="bcryptCost">{t("server:hashing.bcrypt.cost")}</Label>
              <Input
                id="bcryptCost"
                type="number"
                min="10"
                max="31"
                placeholder="12"
                value={bcryptCost}
                onChange={(e) => setBcryptCost(e.target.value)}
                disabled={setParam.isPending}
                data-testid="hashing-bcrypt-cost"
              />
              <p className="text-xs text-muted-foreground">{t("server:hashing.bcrypt.costHelp")}</p>
            </div>
          )}

          <div className="flex items-center justify-between rounded-lg border p-4">
            <div className="space-y-0.5 pr-4">
              <Label htmlFor="rehashOnLogin" className="text-sm font-medium">
                {t("server:hashing.rehash.label")}
              </Label>
              <p className="text-xs text-muted-foreground">{t("server:hashing.rehash.help")}</p>
            </div>
            <Switch
              id="rehashOnLogin"
              checked={rehashOnLogin}
              onCheckedChange={setRehashOnLogin}
              disabled={setParam.isPending}
              data-testid="hashing-rehash"
            />
          </div>

          <Button type="submit" disabled={setParam.isPending} data-testid="hashing-save">
            {setParam.isPending ? (
              <>
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                {t("common:saving")}
              </>
            ) : (
              t("common:save")
            )}
          </Button>
        </CardContent>
      </Card>
    </form>
  );
}
