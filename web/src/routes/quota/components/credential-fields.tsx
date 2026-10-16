import { TriangleAlert } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

/**
 * 按供应商元数据（credential_fields / instructions / warning）动态渲染的
 * 凭据表单段：secret 字段用 type="password"，任何地方不回显明文。
 */
export function QuotaCredentialFields({
  provider,
  values,
  onChange,
  idPrefix,
}: {
  provider: QuotaProvider;
  values: Record<string, string>;
  onChange: (key: string, value: string) => void;
  idPrefix: string;
}) {
  return (
    <div className="space-y-3">
      {provider.instructions.length > 0 && (
        <ol className="list-decimal space-y-1 rounded-md bg-muted/50 px-3 py-2 pl-8 text-xs text-muted-foreground">
          {provider.instructions.map((step) => (
            <li key={step}>{step}</li>
          ))}
        </ol>
      )}
      {provider.warning && (
        <p className="flex items-start gap-1.5 rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-800 dark:text-amber-300">
          <TriangleAlert className="mt-0.5 size-3.5 shrink-0" />
          {provider.warning}
        </p>
      )}
      {provider.credential_fields.map((field) => (
        <div key={field.key} className="space-y-2">
          <Label htmlFor={`${idPrefix}-${field.key}`}>
            {field.label}
            {!field.required && (
              <span className="text-xs font-normal text-muted-foreground">
                （可选）
              </span>
            )}
          </Label>
          <Input
            id={`${idPrefix}-${field.key}`}
            type={field.secret ? "password" : "text"}
            placeholder={field.placeholder}
            autoComplete="off"
            value={values[field.key] ?? ""}
            onChange={(event) => onChange(field.key, event.target.value)}
          />
          {field.help && (
            <p className="text-xs text-muted-foreground">{field.help}</p>
          )}
        </div>
      ))}
    </div>
  );
}
