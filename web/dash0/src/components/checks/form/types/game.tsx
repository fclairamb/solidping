import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { CheckTypeModule } from "./index";
import type { CheckConfig, CheckTypeFieldsProps, FieldErrors } from "./common";
import { getConfigField } from "./common";

const hostRequired = (host: string): FieldErrors =>
  host ? [] : [{ name: "host", message: "Host is required" }];

// ── A2S game server ──
export interface A2sState {
  host: string;
  port: string;
  minPlayers: string;
  maxPlayers: string;
}

export const a2sModule: CheckTypeModule<A2sState> = {
  types: ["a2s"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    port: getConfigField(config, "port"),
    minPlayers: getConfigField(config, "minPlayers"),
    maxPlayers: getConfigField(config, "maxPlayers"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.minPlayers) cfg.minPlayers = parseInt(state.minPlayers, 10);
    if (state.maxPlayers) cfg.maxPlayers = parseInt(state.maxPlayers, 10);
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: A2sFields,
};

function A2sFields({ state, onChange }: CheckTypeFieldsProps<A2sState>) {
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="game.example.com"
            value={state.host}
            onChange={(e) => onChange({ ...state, host: e.target.value })}
            className="flex-1"
          />
          <Input
            id="port"
            type="number"
            placeholder="27015"
            value={state.port}
            onChange={(e) => onChange({ ...state, port: e.target.value })}
            className="w-24"
          />
        </div>
      </div>
      <div className="flex gap-4">
        <div className="space-y-2 flex-1">
          <Label htmlFor="minPlayers">Min Players (optional)</Label>
          <Input
            id="minPlayers"
            type="number"
            min={0}
            placeholder="0"
            value={state.minPlayers}
            onChange={(e) => onChange({ ...state, minPlayers: e.target.value })}
          />
          <p className="text-xs text-muted-foreground">Alert if fewer players</p>
        </div>
        <div className="space-y-2 flex-1">
          <Label htmlFor="maxPlayers">Max Players (optional)</Label>
          <Input
            id="maxPlayers"
            type="number"
            min={0}
            placeholder="0"
            value={state.maxPlayers}
            onChange={(e) => onChange({ ...state, maxPlayers: e.target.value })}
          />
          <p className="text-xs text-muted-foreground">Alert if more players</p>
        </div>
      </div>
    </>
  );
}

// ── Minecraft ──
export interface MinecraftState {
  host: string;
  port: string;
  edition: string;
  minPlayers: string;
  maxPlayers: string;
}

export const minecraftModule: CheckTypeModule<MinecraftState> = {
  types: ["minecraft"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    port: getConfigField(config, "port"),
    edition: getConfigField(config, "edition") || "java",
    minPlayers: getConfigField(config, "minPlayers"),
    maxPlayers: getConfigField(config, "maxPlayers"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.edition && state.edition !== "java") cfg.edition = state.edition;
    if (state.minPlayers) cfg.minPlayers = parseInt(state.minPlayers, 10);
    if (state.maxPlayers) cfg.maxPlayers = parseInt(state.maxPlayers, 10);
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: MinecraftFields,
};

function MinecraftFields({
  state,
  onChange,
}: CheckTypeFieldsProps<MinecraftState>) {
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="play.example.com"
            value={state.host}
            onChange={(e) => onChange({ ...state, host: e.target.value })}
            className="flex-1"
          />
          <Input
            id="port"
            type="number"
            placeholder={state.edition === "bedrock" ? "19132" : "25565"}
            value={state.port}
            onChange={(e) => onChange({ ...state, port: e.target.value })}
            className="w-24"
          />
        </div>
      </div>
      <div className="space-y-2">
        <Label htmlFor="edition">Edition</Label>
        <Select
          value={state.edition}
          onValueChange={(edition) => onChange({ ...state, edition })}
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="java">Java</SelectItem>
            <SelectItem value="bedrock">Bedrock</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div className="flex gap-4">
        <div className="space-y-2 flex-1">
          <Label htmlFor="minPlayers">Min Players (optional)</Label>
          <Input
            id="minPlayers"
            type="number"
            min={0}
            placeholder="0"
            value={state.minPlayers}
            onChange={(e) => onChange({ ...state, minPlayers: e.target.value })}
          />
          <p className="text-xs text-muted-foreground">Alert if fewer players</p>
        </div>
        <div className="space-y-2 flex-1">
          <Label htmlFor="maxPlayers">Max Players (optional)</Label>
          <Input
            id="maxPlayers"
            type="number"
            min={0}
            placeholder="0"
            value={state.maxPlayers}
            onChange={(e) => onChange({ ...state, maxPlayers: e.target.value })}
          />
          <p className="text-xs text-muted-foreground">Alert if more players</p>
        </div>
      </div>
    </>
  );
}
