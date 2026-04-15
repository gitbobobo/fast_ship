import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { screen, fireEvent, waitFor, render } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { ThemeToggle } from "@/components/ui/theme-toggle";
import { ThemeProvider } from "@/components/theme-provider";

// Mock the theme store
const mockSetTheme = vi.fn();
let mockTheme = "system";
let mockResolvedTheme = "light";

vi.mock("@/lib/store/theme-store", () => ({
  useThemeStore: () => ({
    theme: mockTheme,
    resolvedTheme: mockResolvedTheme,
    setTheme: (theme: string) => {
      mockSetTheme(theme);
      mockTheme = theme;
      mockResolvedTheme = theme === "system" ? "light" : theme;
    },
  }),
}));

describe("ThemeToggle Component", () => {
  beforeEach(() => {
    mockTheme = "system";
    mockResolvedTheme = "light";
    mockSetTheme.mockClear();
    document.documentElement.classList.remove("light", "dark");
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("renders theme toggle button", () => {
    render(
      <MemoryRouter>
        <ThemeToggle />
      </MemoryRouter>,
    );

    const buttons = screen.getAllByRole("button");
    const themeButton = buttons.find(btn => 
      btn.querySelector("svg") && btn.getAttribute("aria-haspopup") === "menu"
    );
    expect(themeButton).toBeInTheDocument();
  });

  it("opens dropdown menu when clicked", async () => {
    render(
      <MemoryRouter>
        <ThemeToggle />
      </MemoryRouter>,
    );

    const buttons = screen.getAllByRole("button");
    const themeButton = buttons.find(btn => 
      btn.getAttribute("aria-haspopup") === "menu"
    );
    
    if (themeButton) {
      fireEvent.click(themeButton);

      await waitFor(() => {
        const menuItems = screen.getAllByRole("menuitem");
        const labels = menuItems.map(item => item.textContent);
        expect(labels.some(text => text?.includes("浅色"))).toBe(true);
        expect(labels.some(text => text?.includes("深色"))).toBe(true);
        expect(labels.some(text => text?.includes("跟随系统"))).toBe(true);
      });
    }
  });

  it("changes theme to light when light option is selected", async () => {
    render(
      <MemoryRouter>
        <ThemeToggle />
      </MemoryRouter>,
    );

    const buttons = screen.getAllByRole("button");
    const themeButton = buttons.find(btn => 
      btn.getAttribute("aria-haspopup") === "menu"
    );
    
    if (themeButton) {
      fireEvent.click(themeButton);

      await waitFor(() => {
        expect(screen.getAllByRole("menuitem").length).toBeGreaterThan(0);
      });

      const menuItems = screen.getAllByRole("menuitem");
      const lightOption = menuItems.find(item => item.textContent?.includes("浅色"));
      
      if (lightOption) {
        fireEvent.click(lightOption);
        expect(mockSetTheme).toHaveBeenCalledWith("light");
      }
    }
  });

  it("changes theme to dark when dark option is selected", async () => {
    render(
      <MemoryRouter>
        <ThemeToggle />
      </MemoryRouter>,
    );

    const buttons = screen.getAllByRole("button");
    const themeButton = buttons.find(btn => 
      btn.getAttribute("aria-haspopup") === "menu"
    );
    
    if (themeButton) {
      fireEvent.click(themeButton);

      await waitFor(() => {
        expect(screen.getAllByRole("menuitem").length).toBeGreaterThan(0);
      });

      const menuItems = screen.getAllByRole("menuitem");
      const darkOption = menuItems.find(item => item.textContent?.includes("深色"));
      
      if (darkOption) {
        fireEvent.click(darkOption);
        expect(mockSetTheme).toHaveBeenCalledWith("dark");
      }
    }
  });

  it("changes theme to system when system option is selected", async () => {
    render(
      <MemoryRouter>
        <ThemeToggle />
      </MemoryRouter>,
    );

    const buttons = screen.getAllByRole("button");
    const themeButton = buttons.find(btn => 
      btn.getAttribute("aria-haspopup") === "menu"
    );
    
    if (themeButton) {
      fireEvent.click(themeButton);

      await waitFor(() => {
        expect(screen.getAllByRole("menuitem").length).toBeGreaterThan(0);
      });

      const menuItems = screen.getAllByRole("menuitem");
      const systemOption = menuItems.find(item => item.textContent?.includes("跟随系统"));
      
      if (systemOption) {
        fireEvent.click(systemOption);
        expect(mockSetTheme).toHaveBeenCalledWith("system");
      }
    }
  });

  it("highlights current theme in dropdown", async () => {
    mockTheme = "dark";
    mockResolvedTheme = "dark";

    render(
      <MemoryRouter>
        <ThemeToggle />
      </MemoryRouter>,
    );

    const buttons = screen.getAllByRole("button");
    const themeButton = buttons.find(btn => 
      btn.getAttribute("aria-haspopup") === "menu"
    );
    
    if (themeButton) {
      fireEvent.click(themeButton);

      await waitFor(() => {
        const menuItems = screen.getAllByRole("menuitem");
        const darkOption = menuItems.find(item => item.textContent?.includes("深色"));
        expect(darkOption).toHaveClass("bg-accent");
      });
    }
  });
});

describe("ThemeProvider", () => {
  beforeEach(() => {
    document.documentElement.classList.remove("light", "dark");
    mockTheme = "light";
    mockResolvedTheme = "light";
  });

  it("applies light class to html element when theme is light", () => {
    mockTheme = "light";
    mockResolvedTheme = "light";

    render(
      <MemoryRouter>
        <ThemeProvider>
          <div>Content</div>
        </ThemeProvider>
      </MemoryRouter>,
    );

    expect(document.documentElement.classList.contains("light")).toBe(true);
    expect(document.documentElement.classList.contains("dark")).toBe(false);
  });

  it("applies dark class to html element when theme is dark", () => {
    mockTheme = "dark";
    mockResolvedTheme = "dark";

    render(
      <MemoryRouter>
        <ThemeProvider>
          <div>Content</div>
        </ThemeProvider>
      </MemoryRouter>,
    );

    expect(document.documentElement.classList.contains("dark")).toBe(true);
    expect(document.documentElement.classList.contains("light")).toBe(false);
  });
});
