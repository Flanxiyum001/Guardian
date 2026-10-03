import "./globals.css";

export const metadata = {
  title: "Guardian Dashboard",
  description: "Real-time PII tokenization monitoring gateway",
};

export default function RootLayout({ children }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
