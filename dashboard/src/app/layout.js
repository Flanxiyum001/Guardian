import "./globals.css";
import Nav from "../components/nav";

export const metadata = {
  title: "Guardian Dashboard",
  description: "Real-time PII tokenization, cost and audit monitoring gateway",
};

export default function RootLayout({ children }) {
  return (
    <html lang="en">
      <body>
        <Nav />
        {children}
      </body>
    </html>
  );
}
