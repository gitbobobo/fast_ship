import ReactMarkdown from "react-markdown";
import rehypeRaw from "rehype-raw";
import rehypeSanitize, { defaultSchema } from "rehype-sanitize";
import { useAuthStore } from "@/lib/store/auth-store";
import { rewriteGitHubMediaHtml, toGitHubMediaProxyUrl } from "@/lib/utils/github-media-proxy";

const sanitizeSchema = {
  ...defaultSchema,
  tagNames: [...(defaultSchema.tagNames ?? []), "video", "source"],
  attributes: {
    ...defaultSchema.attributes,
    img: [...(defaultSchema.attributes?.img ?? []), "width", "height"],
    video: [
      ...(defaultSchema.attributes?.video ?? []),
      "src",
      "poster",
      "controls",
      "autoplay",
      "loop",
      "muted",
      "playsinline",
      "preload",
    ],
    source: [...(defaultSchema.attributes?.source ?? []), "src", "type"],
  },
};

interface GitHubContentProps {
  html?: string | null;
  markdown?: string | null;
  className?: string;
}

export function GitHubContent({
  html,
  markdown,
  className,
}: GitHubContentProps) {
  const token = useAuthStore((state) => state.token);
  const rewrittenHtml = rewriteGitHubMediaHtml(html, token);

  if (html?.trim()) {
    return (
      <div
        className={className}
        // body_html comes from GitHub's rendered HTML response, not raw author HTML.
        dangerouslySetInnerHTML={{ __html: rewrittenHtml ?? html }}
      />
    );
  }

  if (markdown?.trim()) {
    return (
      <div className={className}>
        <ReactMarkdown
          components={{
            img: ({ src, ...props }) => <img {...props} src={toGitHubMediaProxyUrl(src, token)} />,
            video: ({ src, poster, ...props }) => (
              <video
                {...props}
                poster={toGitHubMediaProxyUrl(typeof poster === "string" ? poster : undefined, token)}
                src={toGitHubMediaProxyUrl(typeof src === "string" ? src : undefined, token)}
              />
            ),
            source: ({ src, ...props }) => (
              <source {...props} src={toGitHubMediaProxyUrl(typeof src === "string" ? src : undefined, token)} />
            ),
          }}
          rehypePlugins={[rehypeRaw, [rehypeSanitize, sanitizeSchema]]}
        >
          {markdown}
        </ReactMarkdown>
      </div>
    );
  }

  return null;
}
